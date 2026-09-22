# Provider preset sources

Checked against official protocol documentation on 2026-09-22. Presets only fill the endpoint and editable display name; actual models come from the upstream model-list response. They do not imply membership login or complete compatibility with every model capability.

| Preset | OpenAI-compatible base URL | Official source |
| --- | --- | --- |
| DeepSeek | https://api.deepseek.com/v1 | https://api-docs.deepseek.com/quick_start/agent_integrations/nanobot/ |
| OpenAI | https://api.openai.com/v1 | https://platform.openai.com/docs/api-reference/models |
| Groq | https://api.groq.com/openai/v1 | https://console.groq.com/docs/api-reference |
| Mistral | https://api.mistral.ai/v1 | https://docs.mistral.ai/api/endpoint/chat |
| OpenRouter | https://openrouter.ai/api/v1 | https://openrouter.ai/docs/quickstart |

Custom OpenAI-compatible URLs remain editable. No SDK, source, preset database, or assets from another product are copied. Model discovery requests originate in the CPA Cloud service and retain its TLS, redirect, DNS/IP, credential isolation, and administrator authorization requirements.
