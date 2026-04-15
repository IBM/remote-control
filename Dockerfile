FROM --platform=$BUILDPLATFORM golang:1.24 AS build
WORKDIR /src
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build .

FROM registry.access.redhat.com/ubi10/ubi-micro:latest AS release
COPY --from=build /src/remote-control /usr/local/bin/remote-control
ENV HOME=/home/app
RUN mkdir -p /home/app && chmod ugo+rwx /home/app
USER 1423:7986
ENTRYPOINT ["/usr/local/bin/remote-control"]
CMD ["server"]
