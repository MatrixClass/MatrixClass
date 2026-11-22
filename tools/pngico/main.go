package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	inPath := flag.String("in", "", "input PNG")
	outPath := flag.String("out", "", "output ICO")
	flag.Parse()
	if *inPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: pngico -in input.png -out output.ico")
		os.Exit(2)
	}
	in, err := os.Open(*inPath)
	if err != nil {
		panic(err)
	}
	img, err := png.Decode(in)
	_ = in.Close()
	if err != nil {
		panic(err)
	}
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	if width <= 0 || height <= 0 {
		panic("ICO source PNG must have positive dimensions")
	}
	if width != 256 || height != 256 {
		resized := image.NewNRGBA(image.Rect(0, 0, 256, 256))
		for y := range 256 {
			for x := range 256 {
				resized.SetNRGBA(x, y, color.NRGBA{})
			}
		}
		scale := 256.0 / float64(width)
		if candidate := 256.0 / float64(height); candidate < scale {
			scale = candidate
		}
		dstW := int(float64(width) * scale)
		dstH := int(float64(height) * scale)
		offX := (256 - dstW) / 2
		offY := (256 - dstH) / 2
		for y := 0; y < dstH; y++ {
			for x := 0; x < dstW; x++ {
				sx := x * width / dstW
				sy := y * height / dstH
				resized.Set(offX+x, offY+y, img.At(sx, sy))
			}
		}
		img = resized
		width, height = 256, 256
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		panic(err)
	}
	data := pngData.Bytes()
	out, err := os.Create(*outPath)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	write := func(v any) {
		if err := binary.Write(out, binary.LittleEndian, v); err != nil {
			panic(err)
		}
	}
	write(uint16(0))
	write(uint16(1))
	write(uint16(1))
	entryWidth := byte(width)
	if width == 256 {
		entryWidth = 0
	}
	entryHeight := byte(height)
	if height == 256 {
		entryHeight = 0
	}
	write(entryWidth)
	write(entryHeight)
	write(byte(0))
	write(byte(0))
	write(uint16(1))
	write(uint16(32))
	write(uint32(len(data)))
	write(uint32(22))
	if _, err := out.Write(data); err != nil {
		panic(err)
	}
}
