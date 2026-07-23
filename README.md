# Joseph 📚

Une interface web ultra-légère et moderne pour naviguer dans une bibliothèque **BookOrbit**, optimisée spécifiquement pour les liseuses **Kobo**.

Joseph est une alternative moderne à COPS, écrite en **Go** pour la performance et la simplicité. Il ne lit plus aucune base Calibre ni aucun fichier sur disque : tout passe par l'API **OPDS** native de BookOrbit (catalogue, couvertures, téléchargements), en HTTP Basic Auth avec un utilisateur OPDS dédié.

## ✨ Fonctionnalités

  * **Kobo-First :** Interface noire et blanche, gros boutons tactiles, pas de JavaScript complexe.
  * **Ultra-rapide :** Backend en Go (Golang), rendu des pages instantané (SSR), binaire statique (plus de CGO).
  * **Natif BookOrbit :** Catalogue, recherche, couvertures et téléchargements consommés via l'OPDS de BookOrbit — aucune dépendance à Calibre.
  * **Recherche :** Recherche par Titre ou Auteur, filtrage par auteur/série.
  * **Téléchargement :** Priorise le format KEPUB s'il existe, sinon EPUB (BookOrbit gère lui-même la conversion/le nommage).

## Limite connue

L'OPDS de BookOrbit n'expose pas de route "un livre par id", seulement des flux de catalogue paginés/filtrés (qui contiennent déjà tout le détail par entrée). Joseph mémorise donc en RAM chaque livre vu lors du dernier parcours de liste ; la page de détail et le téléchargement s'appuient sur ce cache. En usage normal (parcourir → cliquer un livre) c'est invisible. Un lien direct vers `/book/:id` sans être passé par la liste au préalable (ex: favori navigateur ancien) peut afficher "Livre introuvable" si l'entrée n'est plus en cache — il suffit de revenir à la liste.

## Prérequis côté BookOrbit

1. L'OPDS doit être activé (Réglages > OPDS, activé par défaut).
2. Créer un utilisateur OPDS dédié à Joseph (Réglages > OPDS > Ajouter un utilisateur), distinct du compte web principal — Joseph n'a jamais besoin du mot de passe du compte principal.

## 🚀 Installation dans un LXC (scénario recommandé)

Joseph est prévu pour tourner nativement dans son propre LXC (Proxmox), à côté du LXC BookOrbit — pas de Docker requis, un simple binaire Go + un service systemd, comme le reste des services `dns_xxx`/`port_xxx` de ce style de homelab.

1. Créer un LXC Debian ou Ubuntu (template standard, quelques centaines de Mo suffisent — pas de conversion d'ebook lourde côté Joseph, tout est délégué à BookOrbit).
2. Dans le LXC, en root :

   ```bash
   curl -fsSL https://raw.githubusercontent.com/hug-efrei/joseph/bookorbit-native/install.sh | bash
   ```

   (remplacer `bookorbit-native` par `main` une fois la branche fusionnée). Sans variables d'environnement, le script demande interactivement `BOOKORBIT_URL`, l'utilisateur et le mot de passe OPDS.

   Pour une installation non interactive :

   ```bash
   BOOKORBIT_URL=http://192.168.1.24:3000 \
   BOOKORBIT_OPDS_USER=joseph \
   BOOKORBIT_OPDS_PASSWORD=xxxxx \
   JOSEPH_REF=bookorbit-native \
   bash install.sh
   ```

Le script installe Go si besoin, crée un utilisateur système `joseph` dédié (pas root), compile le binaire, écrit `/opt/joseph/.env` (permissions 600) et un service systemd `joseph.service` (`Restart=on-failure`), puis le démarre. Relancer le script met à jour une installation existante (git pull + rebuild + restart) au lieu d'en recréer une.

Gestion du service :

```bash
systemctl status joseph
journalctl -u joseph -f
```

Pour l'exposer automatiquement via Caddy (convention hve), ajouter les tags `dns_joseph;port_8080` sur le LXC dans Proxmox.

## 🐳 Installation alternative (Docker)

Créez un fichier `docker-compose.yml` :

```yaml
services:
  joseph:
    image: votre-pseudo-dockerhub/joseph:latest
    container_name: joseph
    ports:
      - "8090:8080"
    environment:
      - BOOKORBIT_URL=http://192.168.1.24:3000
      - BOOKORBIT_OPDS_USER=joseph
      - BOOKORBIT_OPDS_PASSWORD=change-me
    volumes:
      - joseph_cache:/data/cache
    restart: unless-stopped

volumes:
  joseph_cache: