package store

import "autoapi/internal/model"

const (
	providerChunkSize = 400
	modelRefChunkSize = 200
)

func chunkStrings(ids []string, size int) [][]string {
	var out [][]string
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}

func chunkModelRefs(refs []model.ProviderModelRef, size int) [][]model.ProviderModelRef {
	var out [][]model.ProviderModelRef
	for i := 0; i < len(refs); i += size {
		end := i + size
		if end > len(refs) {
			end = len(refs)
		}
		out = append(out, refs[i:end])
	}
	return out
}
