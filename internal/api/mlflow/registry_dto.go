package mlflow

import (
	"strconv"

	"github.com/gorevds/litemlflow/internal/model"
)

// ---- Registered Model DTOs --------------------------------------------------

// registeredModelDTO mirrors mlflow.entities.RegisteredModel wire shape.
type registeredModelDTO struct {
	Name           string     `json:"name"`
	CreationTime   int64      `json:"creation_timestamp"`
	LastUpdateTime int64      `json:"last_updated_timestamp"`
	Description    string     `json:"description,omitempty"`
	LatestVersions []mvDTO    `json:"latest_versions,omitempty"`
	Tags           []mvTagDTO `json:"tags,omitempty"`
	Aliases        []aliasDTO `json:"aliases,omitempty"`
}

// aliasDTO is the wire shape for a model alias inside a registered model.
type aliasDTO struct {
	Alias   string `json:"alias"`
	Version string `json:"version"`
}

func registeredModelToDTO(m *model.RegisteredModel) registeredModelDTO {
	tags := make([]mvTagDTO, 0, len(m.Tags))
	for _, t := range m.Tags {
		tags = append(tags, mvTagDTO{Key: t.Key, Value: t.Value})
	}
	lv := make([]mvDTO, 0, len(m.LatestVersions))
	for _, v := range m.LatestVersions {
		lv = append(lv, modelVersionToDTO(v))
	}
	return registeredModelDTO{
		Name:           m.Name,
		CreationTime:   m.CreationTime,
		LastUpdateTime: m.LastUpdateTime,
		Description:    m.Description,
		LatestVersions: lv,
		Tags:           tags,
	}
}

// ---- Model Version DTOs -----------------------------------------------------

// mvDTO mirrors mlflow.entities.ModelVersion wire shape.
type mvDTO struct {
	Name           string     `json:"name"`
	Version        string     `json:"version"` // string in MLflow protocol
	CreationTime   int64      `json:"creation_timestamp"`
	LastUpdateTime int64      `json:"last_updated_timestamp"`
	Description    string     `json:"description,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	CurrentStage   string     `json:"current_stage"`
	Source         string     `json:"source"`
	RunID          string     `json:"run_id,omitempty"`
	Status         string     `json:"status"`
	StatusMessage  string     `json:"status_message,omitempty"`
	Tags           []mvTagDTO `json:"tags,omitempty"`
	RunLink        string     `json:"run_link,omitempty"`
}

// mvTagDTO is the wire shape for model/version tags. Aliased to kvTagDTO
// (defined in dto.go) — wire shape unchanged in T3.12.
type mvTagDTO = kvTagDTO

func modelVersionToDTO(mv *model.ModelVersion) mvDTO {
	tags := make([]mvTagDTO, 0, len(mv.Tags))
	for _, t := range mv.Tags {
		tags = append(tags, mvTagDTO{Key: t.Key, Value: t.Value})
	}
	return mvDTO{
		Name:           mv.Name,
		Version:        strconv.FormatInt(mv.Version, 10),
		CreationTime:   mv.CreationTime,
		LastUpdateTime: mv.LastUpdateTime,
		Description:    mv.Description,
		UserID:         mv.UserID,
		CurrentStage:   mv.CurrentStage,
		Source:         mv.Source,
		RunID:          mv.RunID,
		Status:         mv.Status,
		StatusMessage:  mv.StatusMessage,
		Tags:           tags,
	}
}
