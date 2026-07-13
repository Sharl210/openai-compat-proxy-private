package httpapi

import (
	"context"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

type providerModelsBodies struct {
	raw     []byte
	visible []byte
}

func fetchProviderModelsBodies(ctx context.Context, client *upstream.Client, authorization string, provider config.ProviderConfig) (providerModelsBodies, bool, error) {
	status, body, contentType, err := client.Models(ctx, authorization)
	if err != nil {
		return providerModelsBodies{}, false, err
	}
	if status >= 200 && status < 300 {
		return providerModelsBodies{raw: body, visible: rewriteModelsBody(body, provider)}, true, nil
	}
	if status == http.StatusNotFound {
		fallbackBody, ok := configuredModelsFallbackBody(provider)
		if !ok {
			return providerModelsBodies{}, false, nil
		}
		return providerModelsBodies{visible: fallbackBody}, true, nil
	}
	return providerModelsBodies{}, false, &upstream.HTTPStatusError{
		StatusCode:  status,
		ContentType: contentType,
		BodyBytes:   append([]byte(nil), body...),
		Body:        strings.TrimSpace(string(body)),
	}
}
