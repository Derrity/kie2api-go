# syntax=docker/dockerfile:1.6
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kie2api .

FROM gcr.io/distroless/static-debian12:nonroot
ENV KIE2API_DATA_DIR=/data
WORKDIR /app
COPY --from=build /out/kie2api /app/kie2api
VOLUME ["/data"]
EXPOSE 3001 4142
USER nonroot:nonroot
ENTRYPOINT ["/app/kie2api"]
CMD ["--web-port=3001","--proxy-port=4142","--data-dir=/data"]
