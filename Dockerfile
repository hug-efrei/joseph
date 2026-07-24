# Étape 1 : Compilation
FROM golang:1.25.4-alpine AS builder

WORKDIR /app

# Optimisation du build : on ne copie que les fichiers de deps d'abord
COPY go.mod go.sum ./
RUN go mod download

# Ensuite on copie le code source
COPY . .
# Binaire statique — plus de CGO depuis l'abandon de sqlite3 (Calibre) :
# tout le catalogue transite maintenant par l'API OPDS de BookOrbit.
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-s -w' -o server main.go bookorbit.go

# Étape 2 : Image finale (toute petite)
FROM alpine:3.21

RUN adduser -D -u 1000 joseph
WORKDIR /app

# On copie le binaire et les templates
COPY --from=builder /app/server .
COPY --from=builder /app/templates ./templates

# Pré-crée le point de montage du cache avec le bon propriétaire : le volume
# nommé hérite de cette ownership au premier montage (sinon root, illisible
# pour l'utilisateur joseph non-root).
RUN mkdir -p /data/cache && chown -R joseph:joseph /data/cache

USER joseph

# On expose le port
EXPOSE 8080

# Lancement
CMD ["./server"]