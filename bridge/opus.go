package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tphakala/go-opus/oggopus"
)

const (
	maxDecodedAudioBytes = 1 << 30
	wavHeaderBytes       = 44
	wavMime              = "audio/wav"
)

var errDecodedAudioTooLong = errors.New("decoded audio is too long")

func isOggOpus(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 36)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.HasPrefix(head, []byte("OggS")) && bytes.Equal(head[28:36], []byte("OpusHead"))
}

func wavHeader(channels, sampleRate int, dataBytes uint32) []byte {
	h := make([]byte, wavHeaderBytes)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataBytes)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataBytes)
	return h
}

type cappedWriter struct {
	w    io.Writer
	left int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errDecodedAudioTooLong
	}
	n, err := c.w.Write(p)
	c.left -= int64(n)
	return n, err
}

func decodeOggOpusToWAV(src io.Reader, dst io.WriteSeeker) error {
	dec, err := oggopus.NewDecoder(src)
	if err != nil {
		return err
	}
	info := dec.Info()
	if _, err := dst.Write(wavHeader(info.Channels, int(info.OutputSampleRate), 0)); err != nil {
		return err
	}
	written, err := io.Copy(&cappedWriter{w: dst, left: maxDecodedAudioBytes}, dec)
	if err != nil {
		return err
	}
	if written == 0 {
		return errors.New("no audio decoded")
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = dst.Write(wavHeader(info.Channels, int(info.OutputSampleRate), uint32(written)))
	return err
}

func transcodeOggOpus(dataDir, path string) (string, string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer src.Close()
	f, err := newMediaTemp(dataDir)
	if err != nil {
		return "", "", err
	}
	if err := decodeOggOpusToWAV(src, f); err != nil {
		discardMediaTemp(f)
		return "", "", fmt.Errorf("failed to decode Opus audio: %w", err)
	}
	return finalizeMediaTemp(f, wavMime, "")
}
