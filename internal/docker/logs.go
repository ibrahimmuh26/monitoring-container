package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type LogReader interface {
	Logs(context.Context, Snapshot, time.Time, int, int) (string, bool, error)
}

var fullID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Logs uses the immutable ID captured by the allowlisted inspect. Name reuse
// cannot redirect collection into a replacement container.
func (c *Client) Logs(ctx context.Context, s Snapshot, since time.Time, lines, maxBytes int) (string, bool, error) {
	if !fullID.MatchString(s.ID) || lines < 1 || lines > 2000 || maxBytes < 1 || maxBytes > 128<<10 {
		return "", false, errors.New("invalid log request")
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "follow": {"0"}, "since": {strconv.FormatInt(since.Unix(), 10)}, "tail": {strconv.Itoa(lines)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/containers/"+s.ID+"/logs?"+q.Encode(), nil)
	if err != nil {
		return "", false, ErrUnavailable
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, ErrUnavailable
	}
	// Bound total wire data too: an arbitrary stream of empty frames is not allowed.
	wire := &io.LimitedReader{R: resp.Body, N: int64(maxBytes) + 65536}
	var b strings.Builder
	truncated := false
	if s.TTY {
		data, err := io.ReadAll(io.LimitReader(wire, int64(maxBytes)+1))
		if err != nil {
			return "", false, ErrUnavailable
		}
		if len(data) > maxBytes {
			data = data[:maxBytes]
			truncated = true
		}
		b.Write(data)
	} else {
		for {
			var header [8]byte
			_, err := io.ReadFull(wire, header[:])
			if err == io.EOF {
				if wire.N == 0 {
					truncated = true
				}
				break
			}
			if err != nil {
				return "", false, ErrUnavailable
			}
			if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
				return "", false, ErrUnavailable
			}
			length := int64(binary.BigEndian.Uint32(header[4:]))
			remaining := int64(maxBytes - b.Len())
			take := min(length, remaining)
			if _, err := io.CopyN(&b, wire, take); err != nil {
				return "", false, ErrUnavailable
			}
			if length > remaining || wire.N == 0 {
				truncated = true
				break
			}
		}
	}
	text := b.String()
	if truncated {
		// Never persist a trailing partial line that might bypass redaction.
		if end := strings.LastIndexByte(text, '\n'); end >= 0 {
			text = text[:end+1]
		} else {
			text = ""
		}
	}
	return text, truncated, nil
}
