FROM golang:1.15-alpine
RUN apk add build-base

RUN mkdir /app
ADD . /app
WORKDIR /app

RUN go mod download
RUN go build -o main .
RUN MIGRATION_ONLY=TRUE SOURCE_PATH=data.gz /app/main

CMD ["/app/main"]