# netapp-frontend

A frontend for BuildKit that knows how to build images for .NET Core projects.

## Testing in Development

```
buildctl build --frontend gateway.v0 --frontend-opt=gateway-devel=true --frontend-opt=source=dockerfile.v0  --local gateway-context=<source> --local gateway-dockerfile=<source>/frontend/netapp --local context=. --local dockerfile=. --opt filename=<filename> --opt assembly=<assembly> --opt project=<project> --output type=docker,name=<name> | docker load
```

> NOTE: The `filename`, `assembly`, and `project` options are needed only if overriding the name of the "Dockerfile" or the properties read from it.