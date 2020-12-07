# netapp-frontend

A frontend for BuildKit for building Docker-compatible images of .NET Core projects.

## Manifests (a.k.a Dockerfiles)

This frontend uses a YAML-based manifest file format in place of the standard `Dockerfile` (and can even be named as such).  It has the following format:

```yaml
# syntax = philliphoff/netapp-frontend
assembly: "<publish-relative path to the output assembly>"
configuration: "<MSBuild configuration to build>"
project: "<context-relative path to the project file>"
```

> NOTE: the `# syntax = philliphoff/netapp-frontend` comment *must* be included and placed at the beginning of the file.  The comment is used to indicate to BuildKit which frontend (i.e. `netapp-frontend`) to use when building the image.

## Use

To build .NET images, invoke Docker as you would when using a "normal" `Dockerfile`

If the manifest is named `Dockerfile`:

```sh
> docker build <options> <context>
```

If the manifest is otherwise named:

```sh
> docker build <options> <context>
```

## Testing

To test changes to the frontend, you can have BuildKit create a BuildKit-local build of the frontend image immediately prior to using that frontend to build the target .NET Core project image.

```
buildctl build --frontend gateway.v0 --frontend-opt=gateway-devel=true --frontend-opt=source=dockerfile.v0  --local gateway-context=<source> --local gateway-dockerfile=<source>/frontend/netapp --local context=. --local dockerfile=. --opt filename=<filename> --opt assembly=<assembly> --opt configuration=<configuration> --opt project=<project> --output type=docker,name=<name> | docker load
```

> NOTE: The `filename`, `assembly`, `configuration`, and `project` options are needed only if overriding the name of the "Dockerfile" or the properties read from it.