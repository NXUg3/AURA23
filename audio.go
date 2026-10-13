package reisen

/*
#cgo pkg-config: libavformat libavcodec libavutil libswresample
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavutil/channel_layout.h>
#include <libavutil/version.h>
#include <libswresample/swresample.h>
#include <libswresample/version.h>

#pragma GCC diagnostic ignored "-Wdeprecated-declarations"

// FFmpeg 5.1 introduced the AVChannelLayout API and
// deprecated the old channel_layout/channels fields and
// swr_alloc_set_opts(); FFmpeg 7 removed them entirely.
// These helpers pick the right API at compile time so the
// package builds against FFmpeg 4.x up to 7.x and newer.
#if LIBAVUTIL_VERSION_INT >= AV_VERSION_INT(57, 24, 100) && LIBAVCODEC_VERSION_INT >= AV_VERSION_INT(59, 24, 100) && LIBSWRESAMPLE_VERSION_INT >= AV_VERSION_INT(4, 7, 100)
#define REISEN_NEW_CH_LAYOUT 1
#endif

static int reisen_channel_count(const AVCodecParameters *par) {
#ifdef REISEN_NEW_CH_LAYOUT
	return par->ch_layout.nb_channels;
#else
	return par->channels;
#endif
}

// Creates a resampler converting the stream audio to
// interleaved stereo doubles with the same sample rate.
static SwrContext *reisen_swr_alloc(const AVCodecContext *ctx) {
	SwrContext *swr = NULL;
#ifdef REISEN_NEW_CH_LAYOUT
	AVChannelLayout in_layout = {0};
	AVChannelLayout out_layout = {0};
	int status;

	av_channel_layout_default(&out_layout, 2);
	status = av_channel_layout_copy(&in_layout, &ctx->ch_layout);

	if (status < 0) {
		return NULL;
	}

	// Some streams don't specify the channel order.
	if (in_layout.order == AV_CHANNEL_ORDER_UNSPEC) {
		int channels = in_layout.nb_channels;

		av_channel_layout_uninit(&in_layout);
		av_channel_layout_default(&in_layout, channels);
	}

	status = swr_alloc_set_opts2(&swr,
		&out_layout, AV_SAMPLE_FMT_DBL, ctx->sample_rate,
		&in_layout, ctx->sample_fmt, ctx->sample_rate,
		0, NULL);

	av_channel_layout_uninit(&in_layout);
	av_channel_layout_uninit(&out_layout);

	if (status < 0) {
		return NULL;
	}
#else
	int64_t in_layout = (int64_t)ctx->channel_layout;

	if (in_layout == 0) {
		in_layout = av_get_default_channel_layout(ctx->channels);
	}

	swr = swr_alloc_set_opts(NULL,
		AV_CH_LAYOUT_STEREO, AV_SAMPLE_FMT_DBL, ctx->sample_rate,
		in_layout, ctx->sample_fmt, ctx->sample_rate,
		0, NULL);
#endif
	return swr;
}

// AVFrame.coded_picture_number and display_picture_number
// were removed in FFmpeg 7 (libavutil 59).
static int reisen_coded_picture_number(const AVFrame *frame) {
#if LIBAVUTIL_VERSION_MAJOR < 59
	return frame->coded_picture_number;
#else
	return 0;
#endif
}

static int reisen_display_picture_number(const AVFrame *frame) {
#if LIBAVUTIL_VERSION_MAJOR < 59
	return frame->display_picture_number;
#else
	return 0;
#endif
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

const (
	// StandardChannelCount is used for
	// audio conversion while decoding
	// audio frames.
	StandardChannelCount = 2
)

// AudioStream is a stream containing
// audio frames consisting of audio samples.
type AudioStream struct {
	baseStream
	swrCtx     *C.SwrContext
	buffer     *C.uint8_t
	bufferSize C.int
}

// ChannelCount returns the number of channels
// (1 for mono, 2 for stereo, etc.).
func (audio *AudioStream) ChannelCount() int {
	return int(C.reisen_channel_count(audio.codecParams))
}

// SampleRate returns the sample rate of the
// audio stream.
func (audio *AudioStream) SampleRate() int {
	return int(audio.codecParams.sample_rate)
}

// FrameSize returns the number of samples
// contained in one frame of the audio.
func (audio *AudioStream) FrameSize() int {
	return int(audio.codecParams.frame_size)
}

// Open opens the audio stream to decode
// audio frames and samples from it.
func (audio *AudioStream) Open() error {
	err := audio.open()

	if err != nil {
		return err
	}

	audio.swrCtx = C.reisen_swr_alloc(audio.codecCtx)

	if audio.swrCtx == nil {
		return fmt.Errorf(
			"couldn't allocate an SWR context")
	}

	status := C.swr_init(audio.swrCtx)

	if status < 0 {
		return fmt.Errorf(
			"%d: couldn't initialize the SWR context", status)
	}

	audio.buffer = nil

	return nil
}

// ReadFrame reads a new frame from the stream.
func (audio *AudioStream) ReadFrame() (Frame, bool, error) {
	return audio.ReadAudioFrame()
}

// ReadAudioFrame reads a new audio frame from the stream.
func (audio *AudioStream) ReadAudioFrame() (*AudioFrame, bool, error) {
	ok, err := audio.read()

	if err != nil {
		return nil, false, err
	}

	if ok && audio.skip {
		return nil, true, nil
	}

	// No more data.
	if !ok {
		return nil, false, nil
	}

	maxBufferSize := C.av_samples_get_buffer_size(
		nil, StandardChannelCount,
		audio.frame.nb_samples,
		C.AV_SAMPLE_FMT_DBL, 1)

	if maxBufferSize < 0 {
		return nil, false, fmt.Errorf(
			"%d: couldn't get the max buffer size", maxBufferSize)
	}

	if maxBufferSize > audio.bufferSize {
		C.av_free(unsafe.Pointer(audio.buffer))
		audio.buffer = nil
	}

	if audio.buffer == nil {
		audio.buffer = (*C.uint8_t)(unsafe.Pointer(
			C.av_malloc(bufferSize(maxBufferSize))))
		audio.bufferSize = maxBufferSize

		if audio.buffer == nil {
			return nil, false, fmt.Errorf(
				"couldn't allocate an AV buffer")
		}
	}

	gotSamples := C.swr_convert(audio.swrCtx,
		&audio.buffer, audio.frame.nb_samples,
		&audio.frame.data[0], audio.frame.nb_samples)

	if gotSamples < 0 {
		return nil, false, fmt.Errorf(
			"%d: couldn't convert the audio frame", gotSamples)
	}

	data := C.GoBytes(unsafe.Pointer(
		audio.buffer), maxBufferSize)
	frame := newAudioFrame(audio,
		int64(audio.frame.pts),
		int(C.reisen_coded_picture_number(audio.frame)),
		int(C.reisen_display_picture_number(audio.frame)), data)

	return frame, true, nil
}

// Close closes the audio stream and
// stops decoding audio frames.
func (audio *AudioStream) Close() error {
	err := audio.close()

	if err != nil {
		return err
	}

	C.av_free(unsafe.Pointer(audio.buffer))
	audio.buffer = nil
	C.swr_free(&audio.swrCtx)
	audio.swrCtx = nil

	return nil
}
