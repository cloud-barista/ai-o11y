package model

// Target kinds a request may name, as ai-app names deployment targets.
const (
	TargetKindNode = "node"
	TargetKindK8s  = "k8s"
)

// TargetReq registers a target when its agent is installed. The target is
// named the way ai-app names a deployment target. Uid and the description
// after it are what CB-Tumblebug reports for the node; when Uid is empty they
// are read from CB-Tumblebug.
type TargetReq struct {
	// Kind is "node". "k8s" is reserved and refused with 422.
	Kind    string `json:"kind" enums:"node,k8s" example:"node"`
	NsID    string `json:"nsId" example:"ns01"`
	InfraID string `json:"infraId" example:"infra01"`
	NodeID  string `json:"nodeId" example:"node01"`
	// Uid is the CB-Tumblebug node Uid, the target's key.
	Uid      string `json:"uid,omitempty" example:"d44ikdomt0tac4ejrmmg"`
	Name     string `json:"name,omitempty" example:"web-1"`
	Provider string `json:"provider,omitempty" example:"aws"`
	Region   string `json:"region,omitempty" example:"ap-northeast-2"`
	// OS is "linux", "windows" or empty when unknown.
	OS       string `json:"os,omitempty" enums:"linux,windows" example:"linux"`
	GPUCount int    `json:"gpuCount,omitempty" example:"1"`
	GPUModel string `json:"gpuModel,omitempty" example:"NVIDIA T4"`
	// Attributes are further platform attributes to select the target by.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// TargetInfo is a stored target.
type TargetInfo struct {
	// ID is the target's key: the management platform's own key for it, the
	// CB-Tumblebug node Uid, and the entity_id its series carry.
	ID       string `json:"id" example:"d44ikdomt0tac4ejrmmg"`
	Kind     string `json:"kind" example:"vm"`
	Name     string `json:"name" example:"web-1"`
	Provider string `json:"provider" example:"aws"`
	Region   string `json:"region" example:"ap-northeast-2"`
	OS       string `json:"os" example:"linux"`
	GPUCount int    `json:"gpuCount" example:"1"`
	GPUModel string `json:"gpuModel" example:"NVIDIA T4"`
	// Attributes are the platform attributes, such as nsId, infraId and nodeId.
	Attributes map[string]string `json:"attributes"`
}

// TargetListResp is the target listing body.
type TargetListResp struct {
	Targets []TargetInfo `json:"targets"`
}

// TargetSyncResp is what a sync with the management platform changed.
type TargetSyncResp struct {
	// Updated targets were described again as the platform lists them.
	Updated []string `json:"updated"`
	// Deleted targets were confirmed gone by the platform.
	Deleted []string `json:"deleted"`
	// Kept targets were missing from the listing but not confirmed gone, with
	// the reason.
	Kept map[string]string `json:"kept"`
}
