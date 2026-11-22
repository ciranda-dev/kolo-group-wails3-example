package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	cfg, err := LoadConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Headless {
		if err := runHeadless(context.Background(), cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	app := application.New(application.Options{
		Name:        "Kolo Group",
		Description: "Desktop group chat built with Kolo",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(NewGroupService(cfg)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Kolo Group",
		// Window sized to the golden ratio (1000 / 618 ≈ 1.618).
		Width:  1000,
		Height: 618,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		Linux:            application.LinuxWindow{Icon: appIcon},
		BackgroundColour: application.NewRGB(7, 11, 18),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
