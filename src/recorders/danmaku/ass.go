package danmaku

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

// AssWriter writes danmaku entries to an ASS subtitle file.
type AssWriter struct {
	mu           sync.Mutex
	file         *os.File
	closed       bool
	startAt      time.Time
	cfg          configs.DanmakuConfig
	title        string
	resX         int
	resY         int
	scrollTimeMs int // 滚动总毫秒数
	bannerSpeed  int // ASS Banner speed (ms per pixel)
	laneStart    int // first usable lane index
	laneEnd      int // last usable lane index (exclusive)
	laneNum      int // total lanes in the usable range
	nextLane     int
	laneLast     []int64 // last end time (centiseconds) per lane
}

func parseResolution(res string) (int, int) {
	parts := strings.SplitN(res, "x", 2)
	if len(parts) != 2 {
		return 1920, 1080
	}
	x, err1 := strconv.Atoi(parts[0])
	y, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 1920, 1080
	}
	return x, y
}

func NewAssWriter(filePath string, startAt time.Time, cfg configs.DanmakuConfig, title string) (*AssWriter, error) {
	f, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create ass file: %w", err)
	}

	resX, resY := parseResolution(cfg.Resolution)
	scrollTimeMs := cfg.ScrollTime * 1000
	bannerSpeed := scrollTimeMs / resX
	if bannerSpeed < 1 {
		bannerSpeed = 1
	}

	laneHeight := cfg.FontSize + 4
	totalLanes := resY / laneHeight

	// Determine usable lane range based on scroll_area
	laneStart := 0
	laneEnd := totalLanes
	area := cfg.ScrollArea
	if area == "" {
		area = "full"
	}
	switch area {
	case "top":
		laneEnd = totalLanes / 2
	case "bottom":
		laneStart = totalLanes / 2
	}
	laneNum := laneEnd - laneStart
	if laneNum < 1 {
		laneNum = 1
	}

	w := &AssWriter{
		file:         f,
		startAt:      startAt,
		cfg:          cfg,
		title:        title,
		resX:         resX,
		resY:        resY,
		scrollTimeMs: scrollTimeMs,
		bannerSpeed: bannerSpeed,
		laneStart:   laneStart,
		laneEnd:     laneEnd,
		laneNum:     laneNum,
		nextLane:    0,
		laneLast:    make([]int64, laneNum),
	}

	if err := w.writeHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// scTierColor 返回 SC 价位对应的 ASS 背景色 (B站原始配色)
func scTierColor(price int) string {
	switch {
	case price >= 2000:
		return "&H80E73CC6" // 紫色 #C678F5
	case price >= 1000:
		return "&H80396AFF" // 深红 #FF6A39
	case price >= 500:
		return "&H80396AFF" // 红色 #FF6A39
	case price >= 200:
		return "&H8032A0FF" // 橙色 #FFA032
	case price >= 100:
		return "&H804BB6E3" // 金色 #E3B64B
	case price >= 50:
		return "&H80C7C34F" // 青色 #4FC3C7
	case price >= 30:
		return "&H80B2602A" // 蓝色 #2A60B2
	case price >= 2:
		return "&H80BFBFBF" // 浅灰 #BFBFBF
	default:
		return "&H8014A500" // 默认绿色 #00A514
	}
}

// scTierStyle 返回 SC 价位对应的 ASS 样式名
func scTierStyle(price int) string {
	switch {
	case price >= 2000:
		return "SC2000"
	case price >= 1000:
		return "SC1000"
	case price >= 500:
		return "SC500"
	case price >= 200:
		return "SC200"
	case price >= 100:
		return "SC100"
	case price >= 50:
		return "SC50"
	case price >= 30:
		return "SC30"
	case price >= 2:
		return "SC2"
	default:
		return "SCDefault"
	}
}

func (w *AssWriter) writeHeader() error {
	assAlpha := 255 - w.cfg.Opacity
	backColor := fmt.Sprintf("&H%02X000000&", assAlpha)
	guardBackColor := "&H800080FF"

	// SC 各价位背景色 (B站原始配色)
	sc2 := scTierColor(2)
	sc30 := scTierColor(30)
	sc50 := scTierColor(50)
	sc100 := scTierColor(100)
	sc200 := scTierColor(200)
	sc500 := scTierColor(500)
	sc1000 := scTierColor(1000)
	sc2000 := scTierColor(2000)
	scDefault := scTierColor(0)

	header := fmt.Sprintf(`[Script Info]
Title: %s
ScriptType: v4.00+
WrapStyle: 2
ScaledBorderAndShadow: yes
PlayResX: %d
PlayResY: %d

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Danmaku,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,0,0,0,0,100,100,0,0,1,%d,0,8,0,0,0,1
Style: Gift,%s,%d,&H0000D4FF,&H000000FF,&H00000000,%s,0,0,0,0,100,100,0,0,1,%d,0,8,0,0,0,1
Style: Guard,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,60,1
Style: SC2,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC30,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC50,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC100,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC200,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC500,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC1000,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SC2000,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1
Style: SCDefault,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,%s,1,0,0,0,100,100,0,0,3,1,0,1,0,0,100,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`, w.title, w.resX, w.resY,
		w.cfg.FontName, w.cfg.FontSize, backColor, w.cfg.Outline,
		w.cfg.FontName, w.cfg.FontSize-6, backColor, w.cfg.Outline,
		w.cfg.FontName, w.cfg.FontSize, guardBackColor,
		w.cfg.FontName, w.cfg.FontSize, sc2,
		w.cfg.FontName, w.cfg.FontSize, sc30,
		w.cfg.FontName, w.cfg.FontSize, sc50,
		w.cfg.FontName, w.cfg.FontSize, sc100,
		w.cfg.FontName, w.cfg.FontSize, sc200,
		w.cfg.FontName, w.cfg.FontSize, sc500,
		w.cfg.FontName, w.cfg.FontSize, sc1000,
		w.cfg.FontName, w.cfg.FontSize, sc2000,
		w.cfg.FontName, w.cfg.FontSize, scDefault)
	_, err := w.file.WriteString(header)
	return err
}

func (w *AssWriter) estimateTextWidth(text string) int {
	width := 0
	for _, r := range text {
		if r > 0x7F {
			width += w.cfg.FontSize
		} else {
			width += w.cfg.FontSize / 2
		}
	}
	return width
}

// AddDanmaku appends a single scrolling danmaku line.
func (w *AssWriter) AddDanmaku(recvAt time.Time, username, text string, color int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	elapsed := recvAt.Sub(w.startAt)
	startCS := int64(elapsed / (10 * time.Millisecond))
	if startCS < 0 {
		startCS = 0
	}

	fullText := username + ": " + text
	textWidth := w.estimateTextWidth(fullText)
	totalDistance := w.resX + textWidth
	// 使用 scrollTimeMs 精确计算，避免 bannerSpeed 整数截断
	durationCS := int64(w.scrollTimeMs) * int64(totalDistance) / int64(w.resX) / 10
	if durationCS < 200 {
		durationCS = 200
	}
	endCS := startCS + durationCS

	lane := w.assignLane(startCS, endCS)
	laneHeight := w.cfg.FontSize + 4
	marginV := (lane + w.laneStart) * laneHeight

	if color <= 0 {
		color = 16777215
	}
	assColor := rgbToAssColor(color)

	line := fmt.Sprintf("Dialogue: 0,%s,%s,Danmaku,,0,0,%d,Banner;%d;0;30,{\\c%s}%s\n",
		formatTime(startCS), formatTime(endCS), marginV, w.bannerSpeed, assColor, escapeText(fullText))
	w.file.WriteString(line)
}

// AddGift appends a gift message as a smaller scrolling line.
func (w *AssWriter) AddGift(recvAt time.Time, username, giftName string, num int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	elapsed := recvAt.Sub(w.startAt)
	startCS := int64(elapsed / (10 * time.Millisecond))
	if startCS < 0 {
		startCS = 0
	}

	fullText := fmt.Sprintf("%s 赠送 %s x%d", username, giftName, num)
	textWidth := w.estimateTextWidth(fullText)
	totalDistance := w.resX + textWidth
	durationCS := int64(w.scrollTimeMs) * int64(totalDistance) / int64(w.resX) / 10
	if durationCS < 200 {
		durationCS = 200
	}
	endCS := startCS + durationCS

	lane := w.assignLane(startCS, endCS)
	laneHeight := w.cfg.FontSize + 4
	marginV := (lane + w.laneStart) * laneHeight

	line := fmt.Sprintf("Dialogue: 0,%s,%s,Gift,,0,0,%d,Banner;%d;0;30,%s\n",
		formatTime(startCS), formatTime(endCS), marginV, w.bannerSpeed, escapeText(fullText))
	w.file.WriteString(line)
}

// positionToAlignment maps position string to ASS \an alignment value and margin.
// ASS numpad layout: 7=top-left, 8=top-center, 9=top-right, 4/5/6=middle, 1/2/3=bottom
func positionToAlignment(pos string, bottomMargin int) (alignment int, marginV int) {
	switch pos {
	case "top-left":
		return 7, 20
	case "top-right":
		return 9, 20
	case "bottom-right":
		return 3, bottomMargin
	default: // "bottom-left"
		return 1, bottomMargin
	}
}

// AddGuard appends a guard purchase message.
func (w *AssWriter) AddGuard(recvAt time.Time, username, giftName string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	elapsed := recvAt.Sub(w.startAt)
	startCS := int64(elapsed / (10 * time.Millisecond))
	if startCS < 0 {
		startCS = 0
	}
	endCS := startCS + 500 // 5 seconds

	fullText := fmt.Sprintf("%s %s", username, giftName)
	alignment, marginV := positionToAlignment(w.cfg.GuardPosition, 60)
	line := fmt.Sprintf("Dialogue: 1,%s,%s,Guard,,0,0,%d,,{\\an%d}{\\q0}%s\n",
		formatTime(startCS), formatTime(endCS), marginV, alignment, escapeText(fullText))
	w.file.WriteString(line)
}

// AddSuperChat appends a Super Chat message.
func (w *AssWriter) AddSuperChat(recvAt time.Time, username, text string, price int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	elapsed := recvAt.Sub(w.startAt)
	startCS := int64(elapsed / (10 * time.Millisecond))
	if startCS < 0 {
		startCS = 0
	}
	endCS := startCS + 500 // 5 seconds

	fullText := fmt.Sprintf("[SC ¥%d] %s: %s", price, username, text)
	alignment, marginV := positionToAlignment(w.cfg.ScPosition, 100)
	styleName := scTierStyle(price)
	line := fmt.Sprintf("Dialogue: 1,%s,%s,%s,,0,0,%d,,{\\an%d}{\\q0}%s\n",
		formatTime(startCS), formatTime(endCS), styleName, marginV, alignment, escapeText(fullText))
	w.file.WriteString(line)
}

func (w *AssWriter) assignLane(startCS, endCS int64) int {
	for i := 0; i < w.laneNum; i++ {
		idx := (w.nextLane + i) % w.laneNum
		if w.laneLast[idx] <= startCS {
			w.laneLast[idx] = endCS
			w.nextLane = (idx + 1) % w.laneNum
			return idx
		}
	}
	earliest := 0
	for i := 1; i < w.laneNum; i++ {
		if w.laneLast[i] < w.laneLast[earliest] {
			earliest = i
		}
	}
	w.laneLast[earliest] = endCS
	w.nextLane = (earliest + 1) % w.laneNum
	return earliest
}

func (w *AssWriter) OutputPath() string {
	return w.file.Name()
}

func (w *AssWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func formatTime(cs int64) string {
	h := cs / 360000
	m := (cs % 360000) / 6000
	s := (cs % 6000) / 100
	c := cs % 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, c)
}

func rgbToAssColor(rgb int) string {
	r := (rgb >> 16) & 0xFF
	g := (rgb >> 8) & 0xFF
	b := rgb & 0xFF
	return fmt.Sprintf("&H00%02X%02X%02X&", b, g, r)
}

func escapeText(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			result = append(result, '\\', 'n')
		case '\r':
			// skip
		default:
			result = append(result, s[i])
		}
	}
	return string(result)
}
