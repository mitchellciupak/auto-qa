## Example Scenario

This scenario deploys a small app in Kubernetes:

- `test-api`: a json-server API backed by a ConfigMap (`posts` data)
- `test-ui`: an NGINX-served HTML page with a **Fetch API** button
- `Ingress`: routes `/` to UI and `/api` to API

### Manual Deploy

```bash
kubectl apply -f scenario.yaml
```

### Access Locally

Use port-forward to the UI service:

```bash
kubectl port-forward svc/test-ui -n test-app 8081:80
```

Open: http://localhost:8081/

### UI Preview

The page renders:

```text
Test UI

Fetch API
[
	{
		"id": 1,
		"title": "hello",
		"body": "world"
	},
	{
		"id": 2,
		"title": "kube",
		"body": "test api"
	}
]
```
