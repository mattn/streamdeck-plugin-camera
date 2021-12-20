package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"image"
	"image/jpeg"
	_ "image/png"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"time"

	"gocv.io/x/gocv"
	"golang.org/x/image/draw"

	"github.com/nfnt/resize"
	"github.com/samwho/streamdeck"
	sdcontext "github.com/samwho/streamdeck/context"
)

const (
	imgX = 72
	imgY = 72
)

var (
	maskImg image.Image
	origImg image.Image

	//go:embed data/*
	static embed.FS
)

type PropertyInspectorSettings struct {
	Image string `json:"Image,omitempty"`
}

func init() {
	f, err := static.Open("data/mask.png")
	if err != nil {
		log.Fatal("cannot open mask.png:", err)
	}
	defer f.Close()

	maskImg, _, err = image.Decode(f)
	if err != nil {
		log.Fatal("cannot decode mask.png:", err)
	}
	origImg = maskImg
}

func main() {
	f, err := ioutil.TempFile("", "streamdeck-camera.log")
	if err != nil {
		log.Fatalf("error creating tempfile: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)

	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatalf("%v\n", err)
	}
}

func run(ctx context.Context) error {
	params, err := streamdeck.ParseRegistrationParams(os.Args)
	if err != nil {
		return err
	}

	client := streamdeck.NewClient(ctx, params)
	setup(client)

	return client.Run()
}

func setup(client *streamdeck.Client) {
	action := client.Action("io.github.mattn.streamdeck.camera")

	pi := &PropertyInspectorSettings{}
	contexts := make(map[string]struct{})

	action.RegisterHandler(streamdeck.SendToPlugin, func(ctx context.Context, client *streamdeck.Client, event streamdeck.Event) error {
		err := json.Unmarshal(event.Payload, pi)
		if err != nil {
			return err
		}
		if pi.Image == "" {
			maskImg = origImg
			return nil
		}
		f, err := os.Open(pi.Image)
		if err != nil {
			return err
		}
		defer f.Close()

		img, _, err := image.Decode(f)
		if err != nil {
			return err
		}
		maskImg = img
		return nil
	})

	action.RegisterHandler(streamdeck.WillAppear, func(ctx context.Context, client *streamdeck.Client, event streamdeck.Event) error {
		contexts[event.Context] = struct{}{}
		return nil
	})

	action.RegisterHandler(streamdeck.WillDisappear, func(ctx context.Context, client *streamdeck.Client, event streamdeck.Event) error {
		delete(contexts, event.Context)
		return nil
	})

	exepath, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		cam, err := gocv.OpenVideoCapture(0)
		if err != nil {
			return
		}
		defer cam.Close()

		frame := gocv.NewMat()
		defer frame.Close()

		classifier := gocv.NewCascadeClassifier()
		defer classifier.Close()

		classifier.Load(filepath.Join(filepath.Dir(exepath), "haarcascade_frontalface_default.xml"))

		for range time.Tick(time.Second / 40) {
			if len(contexts) == 0 {
				continue
			}

			if ok := cam.Read(&frame); !ok {
				continue
			}

			nbuf, err := gocv.IMEncode(".jpg", frame)
			if err != nil {
				continue
			}
			img, err := jpeg.Decode(bytes.NewReader(nbuf.GetBytes()))
			if err != nil {
				continue
			}
			rects := classifier.DetectMultiScale(frame)

			canvas := image.NewRGBA(img.Bounds())
			draw.Draw(canvas, img.Bounds(), img, image.Point{0, 0}, draw.Over)
			for _, rect := range rects {
				pt := image.Point{rect.Min.X, rect.Min.Y}
				fimg := resize.Resize(uint(rect.Dx()), uint(rect.Dy()), maskImg, resize.NearestNeighbor)
				draw.Copy(canvas, pt, fimg, fimg.Bounds(), draw.Over, nil)
			}

			resized := resize.Resize(imgX, imgY, canvas, resize.Lanczos3)
			simg, err := streamdeck.Image(resized)
			if err != nil {
				log.Printf("error creating image: %v\n", err)
				continue
			}

			for ctxStr := range contexts {
				ctx := context.Background()
				ctx = sdcontext.WithContext(ctx, ctxStr)
				if err := client.SetImage(ctx, simg, streamdeck.HardwareAndSoftware); err != nil {
					log.Printf("error setting image: %v\n", err)
					continue
				}
			}
		}
	}()
}
