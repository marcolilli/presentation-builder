package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend
var frontendAssets embed.FS

func main() {
	app := NewApp()
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())
	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("Close Window", keys.CmdOrCtrl("w"), func(_ *menu.CallbackData) {
		app.CloseWindow()
	})
	presentationBuilderMenu := appMenu.AddSubmenu("Presentation Builder")
	presentationBuilderMenu.AddText("Settings", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		if err := app.OpenSettings(); err != nil {
			log.Print(err)
		}
	})
	appMenu.Append(menu.EditMenu())
	appMenu.Append(menu.WindowMenu())

	assets, err := fs.Sub(frontendAssets, "frontend")
	if err != nil {
		log.Fatal(err)
	}

	err = wails.Run(&options.App{
		Title:       "Presentation Builder",
		Width:       1200,
		Height:      860,
		MinWidth:    960,
		MinHeight:   720,
		Menu:        appMenu,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   app.startup,
		OnShutdown:  app.shutdown,
		Bind:        []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
