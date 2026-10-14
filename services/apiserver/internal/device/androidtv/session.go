package androidtv

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/apiserver/internal/device"
)

// session is an Android TV driven over ADB, playing through castor's player app.
type session struct {
	addr string
	key  *rsa.PrivateKey
	name string
}

var _ device.Device = (*session)(nil)

func (s *session) run(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	return run(ctx, s.addr, s.key, command, stdin)
}

// formats is what the player is told each container is; it only acts on hls.
var formats = map[mediav1.Container]string{
	mediav1.Container_CONTAINER_HLS:    "hls",
	mediav1.Container_CONTAINER_MP4:    "mp4",
	mediav1.Container_CONTAINER_MPEGTS: "mpegts",
	mediav1.Container_CONTAINER_MKV:    "mkv",
}

// Play starts the player on streamURL, or hands it the new URL when it already runs.
func (s *session) Play(ctx context.Context, streamURL *url.URL, container mediav1.Container) error {
	format, ok := formats[container]
	if !ok {
		return fmt.Errorf("android tv does not support container %v", container)
	}
	out, err := s.run(ctx, strings.Join([]string{
		"am start -W -n", receiverActivity,
		"-a android.intent.action.VIEW",
		"-d", shellQuote(streamURL.String()),
		"--es format", format,
	}, " "), nil)
	if err != nil {
		return fmt.Errorf("starting castor's player: %w", err)
	}
	if text := string(out); strings.Contains(text, "Error") {
		return fmt.Errorf("starting castor's player: %s", strings.TrimSpace(text))
	}
	return nil
}

// The states that end a cast, as the player reports them; it also reports loading, playing and paused, and nothing once closed.
const (
	stateEnded   = "ended"
	failedPrefix = "failed"
)

var broadcastResult = regexp.MustCompile(`Broadcast completed: result=(-?\d+)(?:, data="([^"]*)")?`)

// control sends op to the running player and returns its state, empty when no player answered.
func (s *session) control(ctx context.Context, op string) (string, error) {
	out, err := s.run(ctx, "am broadcast -a "+receiverControl+" -p "+receiverPackage+" --es op "+op, nil)
	if err != nil {
		return "", err
	}
	m := broadcastResult.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("unexpected answer from the TV: %q", strings.TrimSpace(string(out)))
	}
	if string(m[1]) != "1" {
		return "", nil
	}
	return string(m[2]), nil
}

func (s *session) Pause(ctx context.Context) error { return s.hold(ctx, "pause") }

func (s *session) Resume(ctx context.Context) error { return s.hold(ctx, "resume") }

func (s *session) hold(ctx context.Context, op string) error {
	state, err := s.control(ctx, op)
	if err != nil {
		return err
	}
	if state == "" {
		return fmt.Errorf("castor's player is not running on %q, with nothing to %s", s.name, op)
	}
	return nil
}

// Stop closes the player, which ends its playback.
func (s *session) Stop(ctx context.Context) error {
	_, err := s.run(ctx, "am force-stop "+receiverPackage, nil)
	return err
}

// AwaitEnd polls the player until, having played, it ends, closes, or fails.
func (s *session) AwaitEnd(ctx context.Context) error {
	var active bool
	var failure error
	err := device.AwaitPolledEnd(ctx, s.name, "castor's player", func(ctx context.Context) (bool, error) {
		state, err := s.control(ctx, "state")
		if err != nil {
			return false, err
		}
		over, failed := ending(state, active)
		active = active || state != ""
		failure = failed
		return over, nil
	})
	if err != nil {
		return err
	}
	return failure
}

// ending is whether the player in state ends a cast that was already active, and why if it failed.
func ending(state string, active bool) (bool, error) {
	switch {
	case strings.HasPrefix(state, failedPrefix):
		detail := strings.TrimPrefix(strings.TrimPrefix(state, failedPrefix), ":")
		return true, fmt.Errorf("the TV could not play the stream (media error %s)", detail)
	case state == stateEnded:
		return true, nil
	case state == "":
		return active, nil
	}
	return false, nil
}

// Capabilities is what Android's platform player decodes on every Android TV, Fire TV included: H.264 to 1080p and Dolby audio.
func (s *session) Capabilities() *mediav1.Capabilities {
	return &mediav1.Capabilities{
		SelfFetch:  true,
		Containers: []mediav1.Container{mediav1.Container_CONTAINER_HLS, mediav1.Container_CONTAINER_MPEGTS, mediav1.Container_CONTAINER_MP4, mediav1.Container_CONTAINER_MKV},
		// Castor's served HLS is fMP4, which older platform players handle poorly; a TS stream plays everywhere.
		ServedContainer: mediav1.Container_CONTAINER_MPEGTS,
		Video:           []*mediav1.VideoSupport{device.VideoSupport(mediav1.VideoCodec_VIDEO_CODEC_H264)},
		Audio: []*mediav1.AudioSupport{
			{Codec: mediav1.AudioCodec_AUDIO_CODEC_AAC, MaxChannels: 6},
			{Codec: mediav1.AudioCodec_AUDIO_CODEC_AC3},
			{Codec: mediav1.AudioCodec_AUDIO_CODEC_EAC3},
		},
	}
}

func (s *session) Close() error { return nil }
