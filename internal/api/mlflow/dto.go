// Package mlflow implements the MLflow REST API surface.
//
// JSON shapes match the MLflow protobuf-generated wire format so the
// official Python client (and any other MLflow-aware client) interoperates
// without modification. Field names use the snake_case form MLflow emits.
package mlflow

import (
	"strconv"

	"github.com/gorevds/litemlflow/internal/model"
)

// experimentDTO mirrors mlflow.protos.service_pb2.Experiment.
type experimentDTO struct {
	ExperimentID     string             `json:"experiment_id"`
	Name             string             `json:"name"`
	ArtifactLocation string             `json:"artifact_location"`
	LifecycleStage   string             `json:"lifecycle_stage"`
	CreationTime     int64              `json:"creation_time"`
	LastUpdateTime   int64              `json:"last_update_time"`
	Tags             []experimentTagDTO `json:"tags,omitempty"`
}

// kvTagDTO is the unified wire shape for any (key, value) tag pair on the
// MLflow surface — experiment, run, registered-model, model-version,
// dataset-input. Three identical struct definitions previously lived
// here and in registry_dto.go; merged in T3.12 (deep-review proposal).
// Aliases keep the historical names for readability.
type kvTagDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type experimentTagDTO = kvTagDTO

// runInfoDTO mirrors RunInfo. RunInfo + RunData = Run in the protocol.
type runInfoDTO struct {
	RunID          string `json:"run_id"`
	RunUUID        string `json:"run_uuid"` // legacy alias
	RunName        string `json:"run_name,omitempty"`
	ExperimentID   string `json:"experiment_id"`
	UserID         string `json:"user_id,omitempty"`
	Status         string `json:"status"`
	StartTime      int64  `json:"start_time"`
	EndTime        int64  `json:"end_time,omitempty"`
	ArtifactURI    string `json:"artifact_uri"`
	LifecycleStage string `json:"lifecycle_stage"`
}

type runDataDTO struct {
	Metrics []metricDTO `json:"metrics,omitempty"`
	Params  []paramDTO  `json:"params,omitempty"`
	Tags    []tagDTO    `json:"tags,omitempty"`
}

// runInputsDTO mirrors MLflow's RunInputs proto.
// NOTE: inputs lives at the top-level run object, not inside data.
type runInputsDTO struct {
	DatasetInputs []datasetInputDTO `json:"dataset_inputs,omitempty"`
}

type datasetInputDTO struct {
	Dataset datasetDTO `json:"dataset"`
	Tags    []tagDTO   `json:"tags,omitempty"`
}

type datasetDTO struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	SourceType string `json:"source_type,omitempty"`
	Source     string `json:"source,omitempty"`
	Schema     string `json:"schema,omitempty"`
	Profile    string `json:"profile,omitempty"`
}

type runDTO struct {
	Info   runInfoDTO    `json:"info"`
	Data   runDataDTO    `json:"data"`
	Inputs *runInputsDTO `json:"inputs,omitempty"`
}

type metricDTO struct {
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	Step      int64   `json:"step"`
}

type paramDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type tagDTO = kvTagDTO

// Conversion helpers ----------------------------------------------------------

func experimentToDTO(e *model.Experiment) experimentDTO {
	tags := make([]experimentTagDTO, 0, len(e.Tags))
	for _, t := range e.Tags {
		tags = append(tags, experimentTagDTO{Key: t.Key, Value: t.Value})
	}
	return experimentDTO{
		ExperimentID:     strconv.FormatInt(e.ID, 10),
		Name:             e.Name,
		ArtifactLocation: e.ArtifactLocation,
		LifecycleStage:   e.LifecycleStage,
		CreationTime:     e.CreationTime,
		LastUpdateTime:   e.LastUpdateTime,
		Tags:             tags,
	}
}

func runInfoToDTO(r *model.Run) runInfoDTO {
	end := int64(0)
	if r.EndTime != nil {
		end = *r.EndTime
	}
	return runInfoDTO{
		RunID:          r.ID,
		RunUUID:        r.ID,
		RunName:        r.Name,
		ExperimentID:   strconv.FormatInt(r.ExperimentID, 10),
		UserID:         r.UserID,
		Status:         r.Status,
		StartTime:      r.StartTime,
		EndTime:        end,
		ArtifactURI:    r.ArtifactURI,
		LifecycleStage: r.LifecycleStage,
	}
}

func metricsToDTO(ms []model.Metric) []metricDTO {
	out := make([]metricDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, metricDTO{Key: m.Key, Value: m.Value, Timestamp: m.Timestamp, Step: m.Step})
	}
	return out
}

func paramsToDTO(ps []model.Param) []paramDTO {
	out := make([]paramDTO, 0, len(ps))
	for _, p := range ps {
		out = append(out, paramDTO{Key: p.Key, Value: p.Value})
	}
	return out
}

func tagsToDTO(ts []model.KV) []tagDTO {
	out := make([]tagDTO, 0, len(ts))
	for _, t := range ts {
		out = append(out, tagDTO{Key: t.Key, Value: t.Value})
	}
	return out
}

func datasetInputsToDTO(inputs []model.DatasetInput) *runInputsDTO {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]datasetInputDTO, 0, len(inputs))
	for _, inp := range inputs {
		d := datasetDTO{
			Name:       inp.Dataset.Name,
			Digest:     inp.Dataset.Digest,
			SourceType: inp.Dataset.SourceType,
			Source:     inp.Dataset.Source,
			Schema:     inp.Dataset.Schema,
			Profile:    inp.Dataset.Profile,
		}
		tags := tagsToDTO(inp.Tags)
		out = append(out, datasetInputDTO{Dataset: d, Tags: tags})
	}
	return &runInputsDTO{DatasetInputs: out}
}
