FROM registry.access.redhat.com/ubi10/ubi:latest AS build
RUN dnf install -y golang
WORKDIR /src
COPY . .
RUN make

FROM registry.access.redhat.com/ubi10/ubi-micro:latest AS release
COPY --from=build /src/remote-control /usr/local/bin/remote-control
ENV HOME=/home/app
RUN mkdir -p /home/app && chmod ugo+rwx /home/app
USER 1423:7986
ENTRYPOINT ["/usr/local/bin/remote-control"]
