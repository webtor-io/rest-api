build:
	swag init -g services/web.go \
	&& go build .