package adapter

import (
	"fmt"
)

const (
	// prefix for the autogenerated volume names in the task definition
	ecsVolumeNamePrefix = "volume"
)

type Volumes struct {
	VolumeWithHost  map[string]string
	VolumeEmptyHost []string
}

// getVolumeName returns an autogenerated name for the ecs volume
func getVolumeName(suffixNum int) string {
	return fmt.Sprintf("%s-%d", ecsVolumeNamePrefix, suffixNum)
}