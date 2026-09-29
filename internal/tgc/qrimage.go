package tgc

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"time"

	"rsc.io/qr"
)

// QRImage renders the pending login-token link as a PNG QR code for the
// panel: desktop users scan it from a second screen, phone users tap the
// link instead. It fails when no fresh token is waiting for approval.
func (r *Runtime) QRImage() ([]byte, error) {
	url, ok := r.QRTokenURL()
	if !ok {
		return nil, errors.New("qr-посилання відсутнє — спочатку отримайте його")
	}
	return qrImageFor(url)
}

// QRTokenURL returns the pending login link, if still valid.
func (r *Runtime) QRTokenURL() (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.qrURL == "" || time.Now().After(r.qrExpires) {
		return "", false
	}
	return r.qrURL, true
}

func qrImageFor(url string) ([]byte, error) {
	if url == "" {
		return nil, errors.New("qr-посилання відсутнє — спочатку отримайте його")
	}
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return nil, err
	}
	const scale, quiet = 6, 2
	size := code.Size + quiet*2
	img := image.NewGray(image.Rect(0, 0, size*scale, size*scale))
	white := color.Gray{Y: 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, white)
		}
	}
	black := color.Gray{Y: 0}
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if !code.Black(x, y) {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, black)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
