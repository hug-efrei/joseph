# Étape 1 : Compilation
FROM golang:1.25.4-alpine AS builder

# On a besoin de gcc pour sqlite3 (CGO)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Optimisation du build : on ne copie que les fichiers de deps d'abord
COPY go.mod go.sum ./
RUN go mod download

# Ensuite on copie le code source
COPY . .
# Compilation en un binaire statique
RUN CGO_ENABLED=1 GOOS=linux go build -a -ldflags '-linkmode external -extldflags "-static"' -o server main.go

# Étape 2 : Image finale (toute petite)
FROM alpine:latest

WORKDIR /root/

# On copie le binaire et les templates
COPY --from=builder /app/server .
COPY --from=builder /app/templates ./templates

# On expose le port
EXPOSE 8080

# Lancement
CMD ["./server"]