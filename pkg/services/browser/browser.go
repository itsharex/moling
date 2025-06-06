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
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/gojue/moling/pkg/comm"
	"github.com/gojue/moling/pkg/config"
	"github.com/gojue/moling/pkg/services/abstract"
	"github.com/gojue/moling/pkg/utils"
)

const (
	BrowserDataPath                         = "browser" // Path to store browser data
	BrowserServerName comm.MoLingServerType = "Browser"
)

// BrowserServer represents the configuration for the browser service.
type BrowserServer struct {
	abstract.MLService
	config       *BrowserConfig
	name         string // The name of the service
	cancelAlloc  context.CancelFunc
	cancelChrome context.CancelFunc
}

// NewBrowserServer creates a new BrowserServer instance with the given context and configuration.
func NewBrowserServer(ctx context.Context) (abstract.Service, error) {
	bc := NewBrowserConfig()
	globalConf := ctx.Value(comm.MoLingConfigKey).(*config.MoLingConfig)
	bc.BrowserDataPath = filepath.Join(globalConf.BasePath, BrowserDataPath)
	bc.DataPath = filepath.Join(globalConf.BasePath, "data")
	logger, ok := ctx.Value(comm.MoLingLoggerKey).(zerolog.Logger)
	if !ok {
		return nil, fmt.Errorf("BrowserServer: invalid logger type: %T", ctx.Value(comm.MoLingLoggerKey))
	}
	loggerNameHook := zerolog.HookFunc(func(e *zerolog.Event, level zerolog.Level, msg string) {
		e.Str("Service", string(BrowserServerName))
	})
	bs := &BrowserServer{
		MLService: abstract.NewMLService(ctx, logger.Hook(loggerNameHook), globalConf),
		config:    bc,
	}

	err := bs.InitResources()
	if err != nil {
		return nil, err
	}

	return bs, nil
}

// Init initializes the browser server by creating a new context.
func (bs *BrowserServer) Init() error {
	// Initialize the browser server
	err := bs.initBrowser(bs.config.BrowserDataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize browser: %w", err)
	}
	err = utils.CreateDirectory(bs.config.DataPath)
	if err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Create a new context for the browser
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserAgent(bs.config.UserAgent),
		chromedp.Flag("lang", bs.config.DefaultLanguage),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-features", "Translate"),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", true),
		//chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("CommandLineFlagSecurityWarningsEnabled", false),
		chromedp.Flag("disable-notifications", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("autoplay-policy", "user-gesture-required"),
		chromedp.CombinedOutput(bs.Logger),
		// (1920, 1080), (1366, 768), (1440, 900), (1280, 800)
		chromedp.WindowSize(1280, 800),
		chromedp.UserDataDir(bs.config.BrowserDataPath),
		chromedp.IgnoreCertErrors,
	)

	// headless mode
	if bs.config.Headless {
		opts = append(opts, chromedp.Flag("headless", true))
		opts = append(opts, chromedp.Flag("disable-gpu", true))
		opts = append(opts, chromedp.Flag("disable-webgl", true))
	}

	bs.Context, bs.cancelAlloc = chromedp.NewExecAllocator(context.Background(), opts...)

	bs.Context, bs.cancelChrome = chromedp.NewContext(bs.Context,
		chromedp.WithErrorf(bs.Logger.Error().Msgf),
		chromedp.WithDebugf(bs.Logger.Debug().Msgf),
	)

	pe := abstract.PromptEntry{
		PromptVar: mcp.Prompt{
			Name:        "browser_prompt",
			Description: "Get the relevant functions and prompts of the Browser MCP Server",
			//Arguments:   make([]mcp.PromptArgument, 0),
		},
		HandlerFunc: bs.handlePrompt,
	}
	bs.AddPrompt(pe)
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

	bs.AddTool(mcp.NewTool(
		"browser_debug_enable",
		mcp.WithDescription("Enable JavaScript debugging"),
		mcp.WithBoolean("enabled",
			mcp.Description("Enable or disable debugging"),
			mcp.Required(),
		),
	), bs.handleDebugEnable)

	bs.AddTool(mcp.NewTool(
		"browser_set_breakpoint",
		mcp.WithDescription("Set a JavaScript breakpoint"),
		mcp.WithString("url",
			mcp.Description("URL of the script"),
			mcp.Required(),
		),
		mcp.WithNumber("line",
			mcp.Description("Line number"),
			mcp.Required(),
		),
		mcp.WithNumber("column",
			mcp.Description("Column number (optional)"),
		),
		mcp.WithString("condition",
			mcp.Description("Breakpoint condition (optional)"),
		),
	), bs.handleSetBreakpoint)

	bs.AddTool(mcp.NewTool(
		"browser_remove_breakpoint",
		mcp.WithDescription("Remove a JavaScript breakpoint"),
		mcp.WithString("breakpointId",
			mcp.Description("Breakpoint ID to remove"),
			mcp.Required(),
		),
	), bs.handleRemoveBreakpoint)

	bs.AddTool(mcp.NewTool(
		"browser_pause",
		mcp.WithDescription("Pause JavaScript execution"),
	), bs.handlePause)

	bs.AddTool(mcp.NewTool(
		"browser_resume",
		mcp.WithDescription("Resume JavaScript execution"),
	), bs.handleResume)

	bs.AddTool(mcp.NewTool(
		"browser_get_callstack",
		mcp.WithDescription("Get current call stack when paused"),
	), bs.handleGetCallstack)
	return nil
}

// init initializes the browser server by creating the user data directory.
func (bs *BrowserServer) initBrowser(userDataDir string) error {
	_, err := os.Stat(userDataDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat user data directory: %w", err)
	}

	// Check if the directory exists, if it does, we can reuse it
	if err == nil {
		//  判断浏览器运行锁
		singletonLock := filepath.Join(userDataDir, "SingletonLock")
		_, err = os.Stat(singletonLock)
		if err == nil {
			bs.Logger.Debug().Msg("Browser is already running, removing SingletonLock")
			err = os.RemoveAll(singletonLock)
			if err != nil {
				bs.Logger.Error().Str("Lock", singletonLock).Msgf("Browser can't work due to failed removal of SingletonLock: %s", err.Error())
			}
		}
		return nil
	}
	// Create the directory
	err = os.MkdirAll(userDataDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create user data directory: %w", err)
	}
	return nil
}

func (bs *BrowserServer) handlePrompt(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	// 处理浏览器提示
	return &mcp.GetPromptResult{
		Description: "",
		Messages: []mcp.PromptMessage{
			{
				Role: mcp.RoleUser,
				Content: mcp.TextContent{
					Type: "text",
					Text: bs.config.prompt,
				},
			},
		},
	}, nil
}

// handleNavigate handles the navigation action.
func (bs *BrowserServer) handleNavigate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	url, ok := args["url"].(string)
	if !ok {
		return nil, fmt.Errorf("url must be a string")
	}

	err := chromedp.Run(bs.Context, chromedp.Navigate(url))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to navigate: %s", err.Error())), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Navigated to %s", url)), nil
}

// handleScreenshot handles the screenshot action.
func (bs *BrowserServer) handleScreenshot(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	name, ok := args["name"].(string)
	if !ok {
		return mcp.NewToolResultError("name must be a string"), nil
	}
	selector, _ := args["selector"].(string)
	width, _ := args["width"].(int)
	height, _ := args["height"].(int)
	if width == 0 {
		width = 1280
	}
	if height == 0 {
		height = 800
	}
	var buf []byte
	var err error
	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	if selector == "" {
		err = chromedp.Run(runCtx, chromedp.FullScreenshot(&buf, 90))
	} else {
		err = chromedp.Run(bs.Context, chromedp.Screenshot(selector, &buf, chromedp.NodeVisible))
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to take screenshot: %s", err.Error())), nil
	}

	newName := filepath.Join(bs.config.DataPath, fmt.Sprintf("%s_%d.png", strings.TrimRight(name, ".png"), rand.Int()))
	err = os.WriteFile(newName, buf, 0644)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to save screenshot: %s", err.Error())), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Screenshot saved to:%s", newName)), nil
}

// handleClick handles the click action on a specified element.
func (bs *BrowserServer) handleClick(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	selector, ok := args["selector"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("selector must be a string:%v", selector)), nil
	}
	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	err := chromedp.Run(runCtx,
		chromedp.WaitReady("body", chromedp.ByQuery), // 等待页面就绪
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.NodeVisible),
	)
	if err != nil {
		return mcp.NewToolResultError(fmt.Errorf("failed to click element: %s", err.Error()).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Clicked element %s", selector)), nil
}

// handleFill handles the fill action on a specified input field.
func (bs *BrowserServer) handleFill(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	selector, ok := args["selector"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fill selector:%v", args["selector"])), nil
	}

	value, ok := args["value"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fill input field: %v, selector:%v", args["value"], selector)), nil
	}

	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	err := chromedp.Run(runCtx, chromedp.SendKeys(selector, value, chromedp.NodeVisible))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fill input field: %s", err.Error())), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Filled input %s with value %s", selector, value)), nil
}

func (bs *BrowserServer) handleSelect(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	selector, ok := args["selector"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("failed to select selector:%v", args["selector"])), nil
	}
	value, ok := args["value"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("failed to select value:%v", args["value"])), nil
	}
	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	err := chromedp.Run(runCtx, chromedp.SetValue(selector, value, chromedp.NodeVisible))
	if err != nil {
		return mcp.NewToolResultError(fmt.Errorf("failed to select value: %s", err.Error()).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Selected value %s for element %s", value, selector)), nil
}

// handleHover handles the hover action on a specified element.
func (bs *BrowserServer) handleHover(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	selector, ok := args["selector"].(string)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("selector must be a string:%v", selector)), nil
	}
	var res bool
	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	err := chromedp.Run(runCtx, chromedp.Evaluate(`document.querySelector('`+selector+`').dispatchEvent(new Event('mouseover'))`, &res))
	if err != nil {
		return mcp.NewToolResultError(fmt.Errorf("failed to hover over element: %s", err.Error()).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Hovered over element %s, result:%t", selector, res)), nil
}

func (bs *BrowserServer) handleEvaluate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	script, ok := args["script"].(string)
	if !ok {
		return mcp.NewToolResultError("script must be a string"), nil
	}
	var result any
	runCtx, cancelFunc := context.WithTimeout(bs.Context, time.Duration(bs.config.SelectorQueryTimeout)*time.Second)
	defer cancelFunc()
	err := chromedp.Run(runCtx, chromedp.Evaluate(script, &result))
	if err != nil {
		return mcp.NewToolResultError(fmt.Errorf("failed to execute script: %s", err.Error()).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Script executed successfully: %v", result)), nil
}

func (bs *BrowserServer) Close() error {
	bs.Logger.Debug().Msg("Closing browser server")
	bs.cancelAlloc()
	bs.cancelChrome()
	// Cancel the context to stop the browser
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return chromedp.Cancel(ctx)
}

// Config returns the configuration of the service as a string.
func (bs *BrowserServer) Config() string {
	cfg, err := json.Marshal(bs.config)
	if err != nil {
		bs.Logger.Err(err).Msg("failed to marshal config")
		return "{}"
	}
	return string(cfg)
}

func (bs *BrowserServer) Name() comm.MoLingServerType {
	return BrowserServerName
}

// LoadConfig loads the configuration from a JSON object.
func (bs *BrowserServer) LoadConfig(jsonData map[string]any) error {
	err := utils.MergeJSONToStruct(bs.config, jsonData)
	if err != nil {
		return err
	}
	return bs.config.Check()
}
