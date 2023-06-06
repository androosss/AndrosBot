FROM golang:1.20-bullseye

WORKDIR /app
RUN apt update
RUN apt -y install ffmpeg
RUN apt-get -y install libopus-dev libopusfile-dev

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./

RUN go build -o /andros_go

EXPOSE 8080

ENTRYPOINT [ "/andros_go" ]