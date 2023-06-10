FROM golang:1.20-alpine

WORKDIR /app
RUN apk update
RUN apk add musl-dev
RUN apk add gcc
RUN apk add ffmpeg
RUN apk add opus-dev

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./

RUN go build -o /andros_go -tags nolibopusfile

EXPOSE 8080

ENTRYPOINT [ "/andros_go" ]