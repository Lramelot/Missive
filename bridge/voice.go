package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/braheezy/shine-mp3/pkg/mp3"
)

const (
	maxVoiceBytes      = 64 << 20
	maxVoiceDuration   = 10 * time.Minute
	voiceWaveformBars  = 40
	voiceMime          = "audio/mpeg"
	waveformSampleRate = 10
	mp3EncodeRate      = 24000
	mp3FrameSamples    = 576
)

var mp3SampleRates = map[int]bool{8000: true, 11025: true, 12000: true, 16000: true, 22050: true, 24000: true, 32000: true, 44100: true, 48000: true}

type pcmAudio struct {
	Samples    []int16
	SampleRate int
}

func (a pcmAudio) DurationMS() int64 {
	if a.SampleRate == 0 {
		return 0
	}
	return int64(len(a.Samples)) * 1000 / int64(a.SampleRate)
}

func readWAV(path string) (pcmAudio, error) {
	f, err := os.Open(path)
	if err != nil {
		return pcmAudio{}, newError(CodeBadRequest, "voice file is unreadable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > maxVoiceBytes {
		return pcmAudio{}, newError(CodeBadRequest, "voice file is too large")
	}
	return decodeWAV(io.LimitReader(f, maxVoiceBytes))
}

func decodeWAV(r io.Reader) (pcmAudio, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return pcmAudio{}, newError(CodeBadRequest, "voice file is unreadable")
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return pcmAudio{}, newError(CodeBadRequest, "voice file is not a WAV file")
	}
	var format, channels, bits uint16
	var rate uint32
	var pcm []byte
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		end := body + size
		if size < 0 || end > len(data) {
			end = len(data)
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return pcmAudio{}, newError(CodeBadRequest, "invalid WAV format chunk")
			}
			format = binary.LittleEndian.Uint16(data[body:])
			channels = binary.LittleEndian.Uint16(data[body+2:])
			rate = binary.LittleEndian.Uint32(data[body+4:])
			bits = binary.LittleEndian.Uint16(data[body+14:])
		case "data":
			pcm = data[body:end]
		}
		pos = body + size + size%2
	}
	if format != 1 || bits != 16 || (channels != 1 && channels != 2) {
		return pcmAudio{}, newError(CodeBadRequest, "voice file must be 16-bit PCM WAV")
	}
	if !mp3SampleRates[int(rate)] {
		return pcmAudio{}, newError(CodeBadRequest, "unsupported voice sample rate %d Hz", rate)
	}
	frames := len(pcm) / 2 / int(channels)
	if frames == 0 {
		return pcmAudio{}, newError(CodeBadRequest, "voice file is empty")
	}
	if time.Duration(frames)*time.Second/time.Duration(rate) > maxVoiceDuration {
		return pcmAudio{}, newError(CodeBadRequest, "voice message is too long")
	}
	samples := make([]int16, frames)
	for i := range samples {
		if channels == 1 {
			samples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
			continue
		}
		left := int32(int16(binary.LittleEndian.Uint16(pcm[i*4:])))
		right := int32(int16(binary.LittleEndian.Uint16(pcm[i*4+2:])))
		samples[i] = int16((left + right) / 2)
	}
	return pcmAudio{Samples: samples, SampleRate: int(rate)}, nil
}

func resampleLinear(audio pcmAudio, rate int) pcmAudio {
	if audio.SampleRate == rate || audio.SampleRate <= 0 || len(audio.Samples) == 0 {
		return pcmAudio{Samples: audio.Samples, SampleRate: rate}
	}
	n := int(int64(len(audio.Samples)) * int64(rate) / int64(audio.SampleRate))
	out := make([]int16, max(n, 1))
	step := float64(audio.SampleRate) / float64(rate)
	last := len(audio.Samples) - 1
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		if j >= last {
			out[i] = audio.Samples[last]
			continue
		}
		frac := pos - float64(j)
		v := float64(audio.Samples[j])*(1-frac) + float64(audio.Samples[j+1])*frac
		out[i] = int16(math.Round(v))
	}
	return pcmAudio{Samples: out, SampleRate: rate}
}

func encodeMP3(audio pcmAudio) (out []byte, err error) {
	if !mp3SampleRates[audio.SampleRate] {
		return nil, fmt.Errorf("unsupported sample rate %d", audio.SampleRate)
	}
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("mp3 encoder failed: %v", p)
		}
	}()
	audio = resampleLinear(audio, mp3EncodeRate)
	enc := mp3.NewEncoder(mp3EncodeRate, 1)
	frame := mp3FrameSamples
	padded := len(audio.Samples)
	if rem := padded % frame; rem != 0 {
		padded += frame - rem
	}
	samples := make([]int16, padded)
	copy(samples, audio.Samples)
	var buf bytes.Buffer
	if err := enc.Write(&buf, samples); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, errors.New("mp3 encoder produced no data")
	}
	return buf.Bytes(), nil
}

func voiceWaveform(samples []int16, bars int) []float64 {
	if len(samples) == 0 || bars <= 0 {
		return []float64{}
	}
	out := make([]float64, bars)
	peak := 0.0
	for i := 0; i < bars; i++ {
		start := i * len(samples) / bars
		end := max((i+1)*len(samples)/bars, start+1)
		end = min(end, len(samples))
		var sum float64
		for _, s := range samples[start:end] {
			v := float64(s) / 32768
			sum += v * v
		}
		out[i] = math.Sqrt(sum / float64(end-start))
		peak = max(peak, out[i])
	}
	if peak > 0 {
		for i := range out {
			out[i] = math.Round(out[i]/peak*1000) / 1000
		}
	}
	return out
}

func waveformBytes(values []float64) []byte {
	if len(values) == 0 {
		return nil
	}
	out := make([]byte, len(values))
	for i, v := range values {
		out[i] = byte(math.Round(min(max(v, 0), 1) * 100))
	}
	return out
}

func waveformSamplingHz(durationMS int64, bars int) int {
	if durationMS <= 0 || bars == 0 {
		return waveformSampleRate
	}
	return max(1, int(int64(bars)*1000/durationMS))
}

func prepareVoice(path string, durationMS int64) (sendFile, error) {
	audio, err := readWAV(path)
	if err != nil {
		return sendFile{}, err
	}
	encoded, err := encodeMP3(audio)
	if err != nil {
		return sendFile{}, newError(CodeInternal, "failed to encode voice message: %v", err)
	}
	if real := audio.DurationMS(); durationMS <= 0 || durationMS > real+1000 {
		durationMS = real
	}
	return sendFile{
		Path:       path,
		Name:       "audioclip-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + ".mp3",
		Size:       int64(len(encoded)),
		Mime:       voiceMime,
		Kind:       "audio",
		Data:       encoded,
		Voice:      true,
		Waveform:   voiceWaveform(audio.Samples, voiceWaveformBars),
		DurationMS: durationMS,
	}, nil
}

func (b *MetaBackend) SendVoice(ctx context.Context, threadID, path string, durationMS int64, clientID, replyToID string) (string, error) {
	rec, replyTo, err := b.threadAndReply(threadID, replyToID)
	if err != nil {
		return "", err
	}
	file, err := prepareVoice(path, durationMS)
	if err != nil {
		return "", err
	}
	if rec.E2EE {
		return b.sendMediaE2EE(ctx, rec, file, "", replyTo, clientID)
	}
	return b.sendMediaLS(ctx, rec, file, "", replyTo, clientID)
}

func demoToneWAV(seconds float64, rate int) []byte {
	frames := int(seconds * float64(rate))
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+frames*2))
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(frames*2))
	for i := 0; i < frames; i++ {
		t := float64(i) / float64(rate)
		envelope := 0.35 + 0.3*math.Sin(2*math.Pi*1.3*t)
		v := envelope * (math.Sin(2*math.Pi*220*t) + 0.4*math.Sin(2*math.Pi*330*t))
		binary.Write(&buf, binary.LittleEndian, int16(max(-1, min(1, v*0.6))*32000))
	}
	return buf.Bytes()
}

func demoVoiceFile(dataDir string) (string, []float64, int64, error) {
	wav := demoToneWAV(4.2, 16000)
	audio, err := decodeWAV(bytes.NewReader(wav))
	if err != nil {
		return "", nil, 0, err
	}
	encoded, err := encodeMP3(audio)
	if err != nil {
		return "", nil, 0, err
	}
	dir := filepath.Join(dataDir, mediaDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, 0, err
	}
	path := filepath.Join(dir, "demo-voice.mp3")
	if err := os.WriteFile(path, encoded, mediaFilePermission); err != nil {
		return "", nil, 0, err
	}
	return path, voiceWaveform(audio.Samples, voiceWaveformBars), audio.DurationMS(), nil
}
