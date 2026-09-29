package tgc

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
)

func TestValidUsername(t *testing.T) {
	good := []string{"durov", "a1234", "Abc_def890", "z_1_a_b_c_d_e_f_g_h_i_j_k"}
	for _, u := range good {
		if !validUsername(u) {
			t.Errorf("%q should be valid", u)
		}
	}
	bad := []string{"", "ab", "1abc", "ab c", "ab-c", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ab!cd"}
	for _, u := range bad {
		if validUsername(u) {
			t.Errorf("%q should be invalid", u)
		}
	}
}

func TestAvatarMagic(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0x00}
	pngw := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	if !isJPEG(jpeg) || isPNG(jpeg) {
		t.Error("jpeg detect")
	}
	if !isPNG(pngw) || isJPEG(pngw) {
		t.Error("png detect")
	}
	if isJPEG(nil) || isPNG([]byte{1, 2}) {
		t.Error("short input must not match")
	}
}

func TestUpdateValidationWithoutClient(t *testing.T) {
	r := &Runtime{}
	ctx := context.Background()
	if _, err := r.UpdateProfile(ctx, "", "", ""); err == nil {
		t.Error("empty first name must fail before touching the client")
	}
	if _, err := r.UpdateProfile(ctx, "Ok", strings.Repeat("x", 65), ""); err == nil {
		t.Error("long last name must fail")
	}
	if _, err := r.UpdateUsername(ctx, "ab"); err == nil {
		t.Error("short username must fail")
	}
	if _, err := r.UploadAvatar(ctx, "a.jpg", []byte{1, 2, 3}); err == nil {
		t.Error("non-image must fail")
	}
	if _, err := r.UploadAvatar(ctx, "a.jpg", make([]byte, maxAvatar+1)); err == nil {
		t.Error("oversize must fail")
	}
}

func TestQRImageRender(t *testing.T) {
	if _, err := qrImageFor(""); err == nil {
		t.Error("empty URL must fail")
	}
	raw, err := qrImageFor("tg://login?token=test123")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a png: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() || b.Dx() < 100 {
		t.Fatalf("odd size %v", b)
	}
	// Quiet zone corner must be white, first module area dark (finder).
	if !isWhite(img.At(0, 0)) {
		t.Error("quiet zone not white")
	}
	// Finder pattern top-left module block: pixel inside first module.
	found := false
	for y := 12; y < 20; y++ {
		for x := 12; x < 20; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r < 0x8000 && g < 0x8000 && bl < 0x8000 {
				found = true
			}
		}
	}
	if !found {
		t.Error("no dark module found near top-left finder")
	}
}

func isWhite(c interface {
	RGBA() (uint32, uint32, uint32, uint32)
}) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xF000 && g > 0xF000 && b > 0xF000
}
