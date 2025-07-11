# LLM Inference Service E2E Tests

## Configuration Composition Pattern

Tests combine config fragments from different categories to create complete scenarios:
```python
["router-managed", "workload-single-cpu", "model-fb-opt-125m"]
```

The `llm_config_factory` fixture automatically creates/cleans up `LLMInferenceServiceConfig` objects.

## Markers

- `@pytest.mark.llminferenceservice(type="cpu")` - Resource type for selective test execution
- Use `pytest -m "llminferenceservice and cpu"` to run specific resource tests

## Config Naming Convention

Use prefixed categories that get composed together:

- **`workload-*`**: Container specs and resources (e.g., `workload-single-cpu`, `workload-multi-node-gpu`)
- **`model-*`**: Model sources (e.g., `model-fb-opt-125m`, `model-gpt2`) 
- **`router-*`**: Routing configs (e.g., `router-managed`, `router-with-scheduler`)

## Adding New Configs

1. Add to `LLMINFERENCESERVICE_CONFIGS` in `test_configs.py`
2. Follow `category-descriptor` naming (prefix automatically stripped from test IDs) 