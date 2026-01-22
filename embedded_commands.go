package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func workspaceExecuteCommand(context *glsp.Context, params *protocol.ExecuteCommandParams) (any, error) {
	if params.Command == "k8s.embeddedContent" {
		if len(params.Arguments) > 0 {
			argBytes, err := json.Marshal(params.Arguments[0])
			if err != nil {
				return nil, err
			}

			var embeddedParams EmbeddedContentParams
			if err := json.Unmarshal(argBytes, &embeddedParams); err != nil {
				return nil, err
			}

			return handleEmbeddedContent(context, &embeddedParams)
		}
	} else if params.Command == "k8s.saveEmbeddedContent" {
		if len(params.Arguments) > 0 {
			argBytes, err := json.Marshal(params.Arguments[0])
			if err != nil {
				return nil, err
			}

			var saveParams SaveEmbeddedContentParams
			if err := json.Unmarshal(argBytes, &saveParams); err != nil {
				return nil, err
			}

			return handleSaveEmbeddedContent(context, &saveParams)
		}
	}
	return nil, nil
}

type EmbeddedContentParams struct {
	URI string `json:"uri"`
}

type SaveEmbeddedContentParams struct {
	URI     string `json:"uri"`
	Content string `json:"content"`
}

func handleSaveEmbeddedContent(context *glsp.Context, params *SaveEmbeddedContentParams) (any, error) {
	log.Debug().Str("uri", params.URI).Msg("Received save embedded content request")
	state.setNotifyContext(context)

	u, err := url.Parse(params.URI)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	sourceEncoded := q.Get("source")
	keyEncoded := q.Get("key")

	embeddedNamespace := u.Host
	configMapName := ""
	if p := strings.Trim(u.Path, "/"); p != "" {
		parts := strings.Split(p, "/")
		if len(parts) >= 1 {
			configMapName = parts[0]
		}
	}

	if sourceEncoded == "" || keyEncoded == "" {
		return nil, fmt.Errorf("missing source or key in URI")
	}

	sourceBytes, err := base64.URLEncoding.DecodeString(sourceEncoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode source: %w", err)
	}
	sourceURI := string(sourceBytes)

	keyBytes, err := base64.URLEncoding.DecodeString(keyEncoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key: %w", err)
	}
	key := string(keyBytes)

	content, _, _ := getOrLoadDocument(sourceURI)

	if content == "" {
		return nil, fmt.Errorf("document not found: %s", sourceURI)
	}

	log.Info().Str("source", sourceURI).Str("key", key).Str("content", params.Content).Msg("Saving embedded content")

	textEdit, err := state.Resolver.BuildEmbeddedContentTextEdit(content, key, params.Content, configMapName, embeddedNamespace)
	if err != nil {
		return nil, err
	}

	edit := protocol.WorkspaceEdit{
		Changes: map[string][]protocol.TextEdit{
			sourceURI: {*textEdit},
		},
	}

	return edit, nil
}

func handleEmbeddedContent(context *glsp.Context, params *EmbeddedContentParams) (string, error) {
	log.Debug().Str("uri", params.URI).Msg("Received embedded content request")
	state.setNotifyContext(context)

	u, err := url.Parse(params.URI)
	if err != nil {
		return "", err
	}

	log.Debug().Str("rawQuery", u.RawQuery).Msg("Parsed URI query")

	q := u.Query()
	sourceEncoded := q.Get("source")
	keyEncoded := q.Get("key")

	embeddedNamespace := u.Host
	configMapName := ""
	if p := strings.Trim(u.Path, "/"); p != "" {
		parts := strings.Split(p, "/")
		if len(parts) >= 1 {
			configMapName = parts[0]
		}
	}

	log.Debug().Str("sourceEncoded", sourceEncoded).Str("keyEncoded", keyEncoded).Msg("Extracted params")

	if sourceEncoded == "" || keyEncoded == "" {
		return "", fmt.Errorf("missing source or key in URI")
	}

	sourceBytes, err := base64.URLEncoding.DecodeString(sourceEncoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode source: %w", err)
	}
	sourceURI := string(sourceBytes)

	keyBytes, err := base64.URLEncoding.DecodeString(keyEncoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode key: %w", err)
	}
	key := string(keyBytes)

	log.Debug().Str("source", sourceURI).Str("key", key).Msg("Decoded params")

	content, _, _ := getOrLoadDocument(sourceURI)

	if content == "" {
		return "", fmt.Errorf("document not found: %s", sourceURI)
	}

	return state.Resolver.ResolveEmbeddedContent(content, key, configMapName, embeddedNamespace)
}
