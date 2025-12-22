# Guide d'installation : Traefik & Proxmox

Ce guide vous aide à configurer Traefik pour découvrir automatiquement vos services (LXC/VM) via les "Notes" de Proxmox.

## 1. Prérequis Proxmox

Traefik a besoin d'un accès API pour lire les informations des conteneurs.

1.  Connectez-vous à votre interface Proxmox.
2.  Allez dans **Datacenter** > **Permissions** > **API Tokens**.
3.  Cliquez sur **Add**.
    *   **User**: `root@pam` (ou un utilisateur dédié).
    *   **Token ID**: `traefik` (par exemple).
    *   **Privilege Separation**: Décochez si vous voulez simplifier (sinon il faut gérer les permissions finement).
4.  Copiez la **Secret Value** immédiatement (elle ne sera plus affichée).
5.  Le token complet ressemble à : `root@pam!traefik=votre-secret-uuid`.

> **Note sur les permissions** : Si vous utilisez un utilisateur dédié, assurez-vous qu'il a les droits `VM.Audit`, `VM.Monitor` et `Sys.Audit` sur les ressources.

## 2. Configuration de Traefik

Modifiez votre fichier `traefik.yml` (configuration statique) pour inclure le plugin. Utilisez le fichier `traefik.yml` fourni dans ce dossier comme modèle.

**Points clés à modifier :**
*   `endpoint`: L'IP de votre Proxmox.
*   `apiToken`: Le token généré à l'étape 1.

### Installation du plugin
Traefik téléchargera automatiquement le plugin au démarrage s'il a accès à internet. Assurez-vous que votre conteneur Traefik a accès au web.

## 3. Exposer un service (Exemple Jellyfin)

Pour exposer un service, il suffit d'ajouter des métadonnées dans les **Notes** (Description) du LXC ou de la VM dans Proxmox.

1.  Sélectionnez votre LXC Jellyfin.
2.  Allez dans l'onglet **Summary** ou **Notes**.
3.  Cliquez sur **Edit Notes**.
4.  Ajoutez la configuration suivante :

```properties
traefik.enable=true
traefik.http.routers.jellyfin.rule=Host(`stream.boudiou.net`)
traefik.http.services.jellyfin.loadbalancer.server.port=8096
```

*Remplacez `8096` par le port réel de votre service si différent.*

## 4. Vérification

1.  Redémarrez votre instance Traefik pour charger le plugin et la nouvelle config.
2.  Regardez les logs de Traefik (`docker logs traefik` ou via la console). Vous devriez voir le chargement du plugin `proxmox`.
3.  Accédez à votre dashboard Traefik (souvent sur le port 8080) pour voir si le routeur `jellyfin` a été créé.
