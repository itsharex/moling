// Copyright 2025 CFC4N <cfc4n.cs@gmail.com>. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Repository: https://github.com/gojue/moling

// Package services provides a set of services for the MoLing application.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/chromedp/chromedp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	BrowserDataPath = "browser" // Path to store browser data
)

// BrowserServer represents the configuration for the browser service.
type BrowserServer struct {
	MLService
	config *BrowserConfig
	name   string // The name of the service
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBrowserServer creates a new BrowserServer instance with the given context and configuration.
func NewBrowserServer(ctx context.Context, args []string) (Service, error) {

	bc := NewBrowserConfig()
	globalConf := ctx.Value(MoLingConfigKey).(*MoLingConfig)
	userDataDir := filepath.Join(globalConf.BasePath, BrowserDataPath)

	bc.DataPath = filepath.Join(globalConf.BasePath, "data")
	logger, ok := ctx.Value(MoLingLoggerKey).(zerolog.Logger)
	if !ok {
		return nil, fmt.Errorf("BrowserServer: invalid logger type: %T", ctx.Value(MoLingLoggerKey))
	}

	loggerNameHook := zerolog.HookFunc(func(e *zerolog.Event, level zerolog.Level, msg string) {
		e.Str("Service", "BrowserServer")
	})

	bs := &BrowserServer{
		config: bc,
	}
	bs.logger = logger.Hook(loggerNameHook)

	err := bs.initBrowser(userDataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize browser: %v", err)
	}
	// Create a new context for the browser
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserAgent(bc.UserAgent),
		chromedp.Flag("lang", bc.DefaultLanguage),

		chromedp.Flag("headless", false),
		// Like in Puppeteer.
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", true),
		chromedp.CombinedOutput(logger),
		chromedp.WindowSize(1312, 848),
		chromedp.UserDataDir(userDataDir),
		chromedp.IgnoreCertErrors,
	)
	bs.ctx, bs.cancel = chromedp.NewExecAllocator(ctx, opts...)

	bs.ctx, bs.cancel = chromedp.NewContext(bs.ctx, chromedp.WithErrorf(logger.Printf))
	err = bs.init()
	if err != nil {
		return nil, err
	}
	bs.AddTool(mcp.NewTool(
		"browser_navigate",
		mcp.WithDescription("Navigate to a URL"),
		mcp.WithString("url",
			mcp.Description("URL to navigate to"),
			mcp.Required(),
		),
	), bs.handleNavigate)
	bs.AddTool(mcp.NewTool(
		"browser_screenshot",
		mcp.WithDescription("Take a screenshot of the current page or a specific element"),
		mcp.WithString("name",
			mcp.Description("Name for the screenshot"),
			mcp.Required(),
		),
		mcp.WithString("selector",
			mcp.Description("CSS selector for element to screenshot"),
		),
		mcp.WithNumber("width",
			mcp.Description("Width in pixels (default: 1700)"),
		),
		mcp.WithNumber("height",
			mcp.Description("Height in pixels (default: 1100)"),
		),
	), bs.handleScreenshot)
	bs.AddTool(mcp.NewTool(
		"browser_click",
		mcp.WithDescription("Click an element on the page"),
		mcp.WithString("selector",
			mcp.Description("CSS selector for element to click"),
			mcp.Required(),
		),
	), bs.handleClick)
	bs.AddTool(mcp.NewTool(
		"browser_fill",
		mcp.WithDescription("Fill out an input field"),
		mcp.WithString("selector",
			mcp.Description("CSS selector for input field"),
			mcp.Required(),
		),
		mcp.WithString("value",
			mcp.Description("Value to fill"),
			mcp.Required(),
		),
	), bs.handleFill)
	bs.AddTool(mcp.NewTool(
		"browser_select",
		mcp.WithDescription("Select an element on the page with Select tag"),
		mcp.WithString("selector",
			mcp.Description("CSS selector for element to select"),
			mcp.Required(),
		),
		mcp.WithString("value",
			mcp.Description("Value to select"),
			mcp.Required(),
		),
	), bs.handleSelect)
	bs.AddTool(mcp.NewTool(
		"browser_hover",
		mcp.WithDescription("Hover an element on the page"),
		mcp.WithString("selector",
			mcp.Description("CSS selector for element to hover"),
			mcp.Required(),
		),
	), bs.handleHover)
	bs.AddTool(mcp.NewTool(
		"browser_evaluate",
		mcp.WithDescription("Execute JavaScript in the browser console"),
		mcp.WithString("script",
			mcp.Description("JavaScript code to execute"),
			mcp.Required(),
		),
	), bs.handleEvaluate)
	return bs, nil
}

func (bs *BrowserServer) handleNavigate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	url, ok := request.Params.Arguments["url"].(string)
	if !ok {
		return nil, fmt.Errorf("url must be a string")
	}

	err := chromedp.Run(bs.ctx, chromedp.Navigate(url))
	if err != nil {
		return bs.CallToolResultErr(fmt.Sprintf("failed to navigate: %v", err)), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Navigated to %s", url)), nil
}

// init initializes the browser server by creating the user data directory.
func (bs *BrowserServer) initBrowser(userDataDir string) error {
	_, err := os.Stat(userDataDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat user data directory: %v", err)
	}

	// Check if the directory exists, if it does, we can reuse it
	if err == nil {
		//  判断浏览器运行锁
		// .moling/browser/SingletonLock
		singletonLock := filepath.Join(userDataDir, "SingletonLock")
		_, err = os.Stat(singletonLock)
		if err == nil {
			bs.logger.Debug().Msg("Browser is already running, removing SingletonLock")
			err = os.RemoveAll(singletonLock)
			if err != nil {
				bs.logger.Error().Str("Lock", singletonLock).Msgf("Browser can't work due to failed removal of SingletonLock: %v", err)
			}
		}
		return nil
	}
	// Create the directory
	err = os.MkdirAll(userDataDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create user data directory: %v", err)
	}
	return nil
}

// handleScreenshot handles the screenshot action.
func (bs *BrowserServer) handleScreenshot(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, ok := request.Params.Arguments["name"].(string)
	if !ok {
		return bs.CallToolResultErr("name must be a string"), nil
	}
	selector, _ := request.Params.Arguments["selector"].(string)
	width, _ := request.Params.Arguments["width"].(int)
	height, _ := request.Params.Arguments["height"].(int)
	if width == 0 {
		width = 1700
	}
	if height == 0 {
		height = 1100
	}
	var buf []byte
	var err error
	if selector == "" {
		err = chromedp.Run(bs.ctx, chromedp.FullScreenshot(&buf, 90))
	} else {
		err = chromedp.Run(bs.ctx, chromedp.Screenshot(selector, &buf, chromedp.NodeVisible, chromedp.ByQuery))
	}
	if err != nil {
		return bs.CallToolResultErr(fmt.Sprintf("failed to take screenshot: %v", err)), nil
	}

	//
	newName := filepath.Join(bs.config.DataPath, fmt.Sprintf("%s_%d.png", strings.TrimRight(name, ".png"), rand.Int()))
	err = os.WriteFile(newName, buf, 0644)
	if err != nil {
		return bs.CallToolResultErr(fmt.Sprintf("failed to save screenshot: %v", err)), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Screenshot saved to:%s", newName)), nil
}

// handleClick handles the click action on a specified element.
func (bs *BrowserServer) handleClick(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	selector, ok := request.Params.Arguments["selector"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("selector must be a string:%v", selector)), nil
	}
	err := chromedp.Run(bs.ctx, chromedp.Click(selector, chromedp.NodeVisible, chromedp.ByQuery))
	if err != nil {
		return bs.CallToolResultErr(fmt.Errorf("failed to click element: %v", err).Error()), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Clicked element %s", selector)), nil
}

// handleFill handles the fill action on a specified input field.
func (bs *BrowserServer) handleFill(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	selector, ok := request.Params.Arguments["selector"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("failed to fill selector:%v", request.Params.Arguments["selector"])), nil
	}
	value, ok := request.Params.Arguments["value"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("failed to fill input field: %v, selector:%v", request.Params.Arguments["value"], selector)), nil
	}
	err := chromedp.Run(bs.ctx, chromedp.SendKeys(selector, value, chromedp.NodeVisible, chromedp.ByQuery))
	if err != nil {
		return bs.CallToolResultErr(fmt.Sprintf("failed to fill input field: %v", err)), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Filled input %s with value %s", selector, value)), nil
}

func (bs *BrowserServer) handleSelect(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	selector, ok := request.Params.Arguments["selector"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("failed to select selector:%v", request.Params.Arguments["selector"])), nil
	}
	value, ok := request.Params.Arguments["value"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("failed to select value:%v", request.Params.Arguments["value"])), nil
	}
	err := chromedp.Run(bs.ctx, chromedp.SetValue(selector, value, chromedp.NodeVisible, chromedp.ByQuery))
	if err != nil {
		return bs.CallToolResultErr(fmt.Errorf("failed to select value: %v", err).Error()), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Selected value %s for element %s", value, selector)), nil
}

// handleHover handles the hover action on a specified element.
func (bs *BrowserServer) handleHover(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	selector, ok := request.Params.Arguments["selector"].(string)
	if !ok {
		return bs.CallToolResultErr(fmt.Sprintf("selector must be a string:%v", selector)), nil
	}
	var res bool
	err := chromedp.Run(bs.ctx, chromedp.Evaluate(`document.querySelector('`+selector+`').dispatchEvent(new Event('mouseover'))`, &res))
	if err != nil {
		return bs.CallToolResultErr(fmt.Errorf("failed to hover over element: %v", err).Error()), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Hovered over element %s, result:%t", selector, res)), nil
}

func (bs *BrowserServer) handleEvaluate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	script, ok := request.Params.Arguments["script"].(string)
	if !ok {
		return bs.CallToolResultErr("script must be a string"), nil
	}
	var result interface{}
	err := chromedp.Run(bs.ctx, chromedp.Evaluate(script, &result))
	if err != nil {
		return bs.CallToolResultErr(fmt.Errorf("failed to execute script: %v", err).Error()), nil
	}
	return bs.CallToolResult(fmt.Sprintf("Script executed successfully: %v", result)), nil
}

func (bs *BrowserServer) Close() error {
	bs.logger.Debug().Msg("Closing browser server")
	tctx, tcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer tcancel()
	// Cancel the context to stop the browser
	return chromedp.Cancel(tctx)
}

// Config returns the configuration of the service as a string.
func (mls *BrowserServer) Config() string {
	cfg, err := json.Marshal(mls.config)
	if err != nil {
		mls.logger.Err(err).Msg("failed to marshal config")
		return "{}"
	}
	return string(cfg)
}

func (cs *BrowserServer) Name() string {
	return "BrowserServer"
}

func init() {
	RegisterServ(NewBrowserServer)
}
