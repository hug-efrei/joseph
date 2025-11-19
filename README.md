# Joseph 📚

Une interface web ultra-légère et moderne pour naviguer dans une bibliothèque Calibre, optimisée spécifiquement pour les liseuses **Kobo**.

Joseph est une alternative moderne à COPS, écrite en **Go** pour la performance et la simplicité.

## ✨ Fonctionnalités

  * **Kobo-First :** Interface noire et blanche, gros boutons tactiles, pas de JavaScript complexe.
  * **Ultra-rapide :** Backend en Go (Golang), rendu des pages instantané (SSR).
  * **Smart Cover :** Redimensionnement et optimisation des couvertures à la volée pour les écrans E-Ink HD.
  * **Recherche :** Recherche par Titre ou Auteur.
  * **Téléchargement :** Priorise le format KEPUB s'il existe, sinon EPUB.

## 🚀 Installation rapide (Docker)

Créez un fichier `docker-compose.yml` :

```yaml
services:
  joseph:
    image: votre-pseudo-dockerhub/joseph:latest
    container_name: joseph
    ports:
      - "8090:8080"
    volumes:
      - /chemin/vers/votre/bibliotheque/Calibre:/books:ro
    restart: unless-stopped