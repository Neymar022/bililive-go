package servers

import (
	"encoding/json"
	"net/url"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

func RegisterSubtitleHandlers(r *mux.Router) {
	r.HandleFunc("/subtitles/records", listSubtitleRecords).Methods("GET")
	r.HandleFunc("/subtitles/records/{path:.*}", getSubtitleRecord).Methods("GET")
	r.HandleFunc("/subtitles/records/{path:.*}/rerun", rerunSubtitleRecord).Methods("POST")
	r.HandleFunc("/subtitles/records/{path:.*}/keep-source", updateSubtitleKeepSource).Methods("POST")
	r.HandleFunc("/subtitles/records/{path:.*}/source", deleteSubtitleSource).Methods("DELETE")
	r.HandleFunc("/subtitles/settings", getSubtitleSettings).Methods("GET")
	r.HandleFunc("/subtitles/settings", putSubtitleSettings).Methods("PUT")
	r.HandleFunc("/subtitles/style-lab/settings", getSubtitleStyleLabSettings).Methods("GET")
	r.HandleFunc("/subtitles/style-lab/settings", putSubtitleStyleLabSettings).Methods("PUT")
	r.HandleFunc("/subtitles/style-lab/preview", previewSubtitleStyleLab).Methods("POST")
	r.HandleFunc("/subtitles/style-lab/sample", sampleSubtitleStyleLab).Methods("POST")
	r.HandleFunc("/subtitles/assets/{path:.*}", getSubtitleAsset).Methods("GET")
}

func listSubtitleRecords(writer http.ResponseWriter, r *http.Request) {
	sourceRoot, libraryRoot, retentionDays := getSubtitleRoots()
	records, err := subtitle.ListRecords(libraryRoot, sourceRoot, retentionDays)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}
	writeJSON(writer, commonResp{Data: records})
}

func getSubtitleRecord(writer http.ResponseWriter, r *http.Request) {
	sourceRoot, libraryRoot, retentionDays := getSubtitleRoots()
	relativePath := mux.Vars(r)["path"]
	record, err := subtitle.GetRecord(libraryRoot, sourceRoot, relativePath, retentionDays)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}
	writeJSON(writer, commonResp{Data: record})
}

func rerunSubtitleRecord(writer http.ResponseWriter, r *http.Request) {
	inst := instance.GetInstance(r.Context())
	pm := pipeline.GetManager(inst)
	if pm == nil {
		writeJSON(writer, commonResp{ErrMsg: "字幕任务队列不可用"})
		return
	}

	sourceRoot, libraryRoot, _ := getSubtitleRoots()
	relativePath := mux.Vars(r)["path"]
	videoPath, err := getSafePath(libraryRoot, relativePath)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效或越权路径"})
		return
	}

	var body struct {
		Provider string `json:"provider"`
		Preset   string `json:"preset"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	record, err := subtitle.GetRecord(libraryRoot, sourceRoot, relativePath, configs.GetCurrentConfig().Subtitle.RetentionDays)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	sourcePath := record.SourcePath
	if sourcePath == "" {
		sourcePath, err = subtitle.ResolveSourcePath(videoPath, sourceRoot)
		if err != nil {
			writeJSON(writer, commonResp{ErrMsg: err.Error()})
			return
		}
	}

	provider := body.Provider
	if provider == "" {
		provider = configs.GetCurrentConfig().Subtitle.DefaultProvider
	}
	preset := subtitle.ResolveRenderPreset(body.Preset, record.RenderPreset, configs.GetCurrentConfig().Subtitle.BurnStyle)

	task := pipeline.NewPipelineTask(
		pipeline.RecordInfo{
			Platform:  record.Platform,
			HostName:  record.HostName,
			RoomName:  record.RoomName,
			StartTime: derefTime(record.RecordedAt),
		},
		&pipeline.PipelineConfig{
			Stages: []pipeline.StageConfig{
				{
					Name: pipeline.StageNameSubtitleGenerate,
					Options: map[string]any{
						"provider": provider,
						"preset":   preset,
					},
				},
			},
		},
		[]pipeline.FileInfo{
			{
				Path: sourcePath,
				Type: pipeline.FileTypeVideo,
			},
		},
	)
	if err := pm.EnqueueTask(task); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	metadataPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".subtitle.json"
	metadata := subtitle.Metadata{}
	if existing, loadErr := subtitle.LoadMetadata(metadataPath); loadErr == nil {
		metadata = existing
	} else if !os.IsNotExist(loadErr) {
		writeJSON(writer, commonResp{ErrMsg: loadErr.Error()})
		return
	}
	if metadata.RecordMeta == nil {
		metadata.RecordMeta = map[string]any{}
	}
	metadata.Status = subtitle.StatusQueued
	metadata.Provider = provider
	metadata.SourcePath = sourcePath
	metadata.OutputPath = videoPath
	metadata.SRTPath = strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
	metadata.SourceExists = fileExistsOnDisk(sourcePath)
	metadata.LastError = ""
	metadata.RenderPreset = preset
	metadata.RendererStatus = subtitle.StatusQueued
	metadata.RendererError = ""
	metadata.Segments = nil
	metadata.CompletedAt = nil
	metadata.SourceDeletedAt = nil
	metadata.RecordMeta["platform"] = record.Platform
	metadata.RecordMeta["host_name"] = record.HostName
	metadata.RecordMeta["room_name"] = record.RoomName
	metadata.RecordMeta["start_time"] = formatOptionalTime(record.RecordedAt)
	if err := subtitle.SaveMetadata(metadataPath, metadata); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	writeJSON(writer, commonResp{Data: map[string]any{"task_id": task.ID}})
}

func updateSubtitleKeepSource(writer http.ResponseWriter, r *http.Request) {
	_, libraryRoot, _ := getSubtitleRoots()
	relativePath := mux.Vars(r)["path"]
	videoPath, err := getSafePath(libraryRoot, relativePath)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效或越权路径"})
		return
	}

	var body struct {
		KeepSource bool `json:"keep_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效请求"})
		return
	}

	if err := subtitle.SetKeepSource(videoPath, body.KeepSource); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}
	writeJSON(writer, commonResp{Data: "OK"})
}

func deleteSubtitleSource(writer http.ResponseWriter, r *http.Request) {
	sourceRoot, libraryRoot, _ := getSubtitleRoots()
	relativePath := mux.Vars(r)["path"]
	videoPath, err := getSafePath(libraryRoot, relativePath)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效或越权路径"})
		return
	}

	if err := subtitle.DeleteSourceFile(videoPath, sourceRoot); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}
	writeJSON(writer, commonResp{Data: "OK"})
}

func getSubtitleSettings(writer http.ResponseWriter, r *http.Request) {
	cfg := configs.GetCurrentConfig()
	writeJSON(writer, commonResp{Data: map[string]any{
		"subtitle":     cfg.Subtitle,
		"source_root":  cfg.Subtitle.GetEffectiveSourceRoot(cfg.OutPutPath),
		"library_root": cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath),
		"worker_url":   cfg.Subtitle.GetWorkerURL(),
	}})
}

func putSubtitleSettings(writer http.ResponseWriter, r *http.Request) {
	var body struct {
		Subtitle configs.SubtitleConfig `json:"subtitle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效请求"})
		return
	}
	body.Subtitle.UpdatedAt = time.Now().UTC()

	if _, err := configs.Update(func(c *configs.Config) error {
		c.Subtitle = body.Subtitle
		return c.Verify()
	}); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}
	writeJSON(writer, commonResp{Data: "OK"})
}

func getSubtitleStyleLabSettings(writer http.ResponseWriter, r *http.Request) {
	cfg := configs.GetCurrentConfig()
	writeJSON(writer, commonResp{Data: map[string]any{
		"burn_style": cfg.Subtitle.BurnStyle,
	}})
}

func putSubtitleStyleLabSettings(writer http.ResponseWriter, r *http.Request) {
	var body struct {
		BurnStyle configs.SubtitleBurnStyle `json:"burn_style"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效请求"})
		return
	}

	if _, err := configs.Update(func(c *configs.Config) error {
		c.Subtitle.BurnStyle = body.BurnStyle
		c.Subtitle.UpdatedAt = time.Now().UTC()
		return c.Verify()
	}); err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	writeJSON(writer, commonResp{Data: "OK"})
}

func previewSubtitleStyleLab(writer http.ResponseWriter, r *http.Request) {
	cfg := configs.GetCurrentConfig()
	_, libraryRoot, _ := getSubtitleRoots()
	var body subtitle.StyleLabPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效请求"})
		return
	}
	if body.OutputPreviewPath == "" {
		body.OutputPreviewPath = filepath.Join(libraryRoot, ".style-lab-previews", "preview.png")
	}
	body.BurnStyle = mergeBurnStyle(cfg.Subtitle.BurnStyle, body.BurnStyle)

	response, err := subtitle.PreviewStyle(cfg.Subtitle.GetWorkerURL(), body)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	writeJSON(writer, commonResp{Data: map[string]any{
		"preview_image_path": response.PreviewImagePath,
		"preview_image_url":  buildSubtitleAssetURL(libraryRoot, response.PreviewImagePath),
		"render_preset":      response.RenderPreset,
	}})
}

func sampleSubtitleStyleLab(writer http.ResponseWriter, r *http.Request) {
	cfg := configs.GetCurrentConfig()
	_, libraryRoot, _ := getSubtitleRoots()
	var body subtitle.StyleLabSampleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效请求"})
		return
	}
	if body.OutputDir == "" {
		body.OutputDir = filepath.Join(libraryRoot, ".style-lab-samples")
	}
	body.BurnStyle = mergeBurnStyle(cfg.Subtitle.BurnStyle, body.BurnStyle)

	response, err := subtitle.GenerateStyleSample(cfg.Subtitle.GetWorkerURL(), body)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: err.Error()})
		return
	}

	writeJSON(writer, commonResp{Data: map[string]any{
		"sample_video_path": response.SampleVideoPath,
		"sample_srt_path":   response.SampleSRTPath,
		"sample_video_url":  buildSubtitleAssetURL(libraryRoot, response.SampleVideoPath),
		"sample_srt_url":    buildSubtitleAssetURL(libraryRoot, response.SampleSRTPath),
		"render_preset":     response.RenderPreset,
	}})
}

func getSubtitleAsset(writer http.ResponseWriter, r *http.Request) {
	_, libraryRoot, _ := getSubtitleRoots()
	relativePath := mux.Vars(r)["path"]
	assetPath, err := getSafePath(libraryRoot, relativePath)
	if err != nil {
		writeJSON(writer, commonResp{ErrMsg: "无效或越权路径"})
		return
	}
	http.ServeFile(writer, r, assetPath)
}

func getSubtitleRoots() (sourceRoot string, libraryRoot string, retentionDays int) {
	cfg := configs.GetCurrentConfig()
	return cfg.Subtitle.GetEffectiveSourceRoot(cfg.OutPutPath), cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath), cfg.Subtitle.RetentionDays
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func fileExistsOnDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func buildSubtitleAssetURL(libraryRoot string, absolutePath string) string {
	if strings.TrimSpace(libraryRoot) == "" || strings.TrimSpace(absolutePath) == "" {
		return ""
	}
	relativePath, err := filepath.Rel(libraryRoot, absolutePath)
	if err != nil {
		return ""
	}
	if relativePath == "." || strings.HasPrefix(relativePath, "..") {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(relativePath), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return "/api/subtitles/assets/" + strings.Join(parts, "/")
}

func mergeBurnStyle(base configs.SubtitleBurnStyle, override configs.SubtitleBurnStyle) configs.SubtitleBurnStyle {
	merged := base
	if override.Preset != "" {
		merged.Preset = override.Preset
	}
	if override.FontName != "" {
		merged.FontName = override.FontName
	}
	if override.FontSize != 0 {
		merged.FontSize = override.FontSize
	}
	if override.CardWidth != 0 {
		merged.CardWidth = override.CardWidth
	}
	if override.CardHeight != 0 {
		merged.CardHeight = override.CardHeight
	}
	if override.BottomOffset != 0 {
		merged.BottomOffset = override.BottomOffset
	}
	if override.BackgroundOpacity != 0 {
		merged.BackgroundOpacity = override.BackgroundOpacity
	}
	if override.BorderOpacity != 0 {
		merged.BorderOpacity = override.BorderOpacity
	}
	if override.SingleLine != base.SingleLine {
		merged.SingleLine = override.SingleLine
	}
	if override.OverflowMode != "" {
		merged.OverflowMode = override.OverflowMode
	}
	if override.MarginV != 0 {
		merged.MarginV = override.MarginV
	}
	if override.Outline != 0 {
		merged.Outline = override.Outline
	}
	if override.Shadow != 0 {
		merged.Shadow = override.Shadow
	}
	return merged
}
