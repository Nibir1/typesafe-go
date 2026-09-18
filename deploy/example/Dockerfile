# Built from the repository root so the replace directives resolve.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/deploy/example
RUN go build -o /out/example .

FROM alpine:3.22
COPY --from=build /out/example /usr/local/bin/example
ENTRYPOINT ["example"]
