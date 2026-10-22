package imaging_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/imaging"
)

func sampleImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 90, A: 255})
		}
	}
	return img
}

func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, sampleImage(width, height)))
	return buf.Bytes()
}

func TestDecode(t *testing.T) {
	data := samplePNG(t, 10, 20)
	img, mime, err := imaging.Decode(data)
	require.NoError(t, err)
	require.Equal(t, imaging.MimePNG, mime)
	require.Equal(t, 10, img.Bounds().Dx())
	require.Equal(t, 20, img.Bounds().Dy())

	_, _, err = imaging.Decode([]byte("not an image"))
	require.ErrorIs(t, err, imaging.ErrInvalidImage)
}

func TestResizeCoversAndCrops(t *testing.T) {
	wide := imaging.Resize(sampleImage(400, 100), 100, 100)
	width, height := imaging.Dimensions(wide)
	require.Equal(t, 100, width)
	require.Equal(t, 100, height)

	tall := imaging.Resize(sampleImage(100, 400), 300, 300)
	width, height = imaging.Dimensions(tall)
	require.Equal(t, 300, width)
	require.Equal(t, 300, height)

	upscaled := imaging.Resize(sampleImage(20, 20), 100, 100)
	width, height = imaging.Dimensions(upscaled)
	require.Equal(t, 100, width)
	require.Equal(t, 100, height)
}

func TestThumbnails(t *testing.T) {
	thumbs, err := imaging.Thumbnails(sampleImage(640, 480), imaging.DefaultSizes)
	require.NoError(t, err)
	require.Len(t, thumbs, 2)

	for _, thumb := range thumbs {
		require.Equal(t, imaging.MimeJPEG, thumb.MimeType)
		require.NotEmpty(t, thumb.Data)

		img, mime, err := imaging.Decode(thumb.Data)
		require.NoError(t, err)
		require.Equal(t, imaging.MimeJPEG, mime)
		require.Equal(t, thumb.Size.Width, img.Bounds().Dx())
		require.Equal(t, thumb.Size.Height, img.Bounds().Dy())
	}

	require.Equal(t, "100x100", thumbs[0].Size.Name)
	require.Equal(t, "300x300", thumbs[1].Size.Name)
}

func TestEncode(t *testing.T) {
	img := sampleImage(32, 32)

	data, mime, err := imaging.Encode(img, "jpeg")
	require.NoError(t, err)
	require.Equal(t, imaging.MimeJPEG, mime)
	require.NotEmpty(t, data)

	data, mime, err = imaging.Encode(img, "png")
	require.NoError(t, err)
	require.Equal(t, imaging.MimePNG, mime)
	require.NotEmpty(t, data)

	_, _, err = imaging.Encode(img, "webp")
	require.ErrorIs(t, err, imaging.ErrUnsupportedFormat)
}

func TestSniffMimeAndHelpers(t *testing.T) {
	require.Equal(t, imaging.MimePNG, imaging.SniffMime(samplePNG(t, 4, 4)))
	require.Empty(t, imaging.SniffMime(nil))
	require.Empty(t, imaging.SniffMime([]byte{0x00, 0x01, 0x02}))

	require.True(t, imaging.SupportedMime(imaging.MimeJPEG))
	require.True(t, imaging.SupportedMime(imaging.MimePNG))
	require.True(t, imaging.SupportedMime(imaging.MimeWebP))
	require.False(t, imaging.SupportedMime("image/gif"))

	require.Equal(t, ".jpg", imaging.Extension(imaging.MimeJPEG))
	require.Equal(t, ".png", imaging.Extension(imaging.MimePNG))
	require.Equal(t, ".webp", imaging.Extension(imaging.MimeWebP))
	require.Equal(t, ".bin", imaging.Extension("application/pdf"))
}
