# CloudNVR V2

CloudNVR est un NVR personnel multi-sites. Le service cloud gère les sites, les caméras et les politiques de conservation. Un agent installé sur chaque réseau local accède aux URL RTSP privées et maintient une connexion sortante vers le cloud.

Le dépôt contient une version fonctionnelle orientée déploiement personnel :

- une API cloud en Go protégée par une clé d'administration ;
- MariaDB pour les métadonnées ;
- un enrôlement sécurisé des agents par site ;
- le chiffrement AES-256-GCM des URL RTSP dans MariaDB ;
- la synchronisation cloud → agent des caméras et politiques ;
- les modes `local`, `cloud`, `hybrid`, `manual` et `disabled` ;
- les politiques de transfert `all`, `events`, `manual`, `events_and_manual` et `none`.
- une interface web responsive pour administrer les sites, les caméras et leurs politiques.
- un moteur FFmpeg supervisé dans le cloud et dans l'agent ;
- une passerelle MediaMTX sur le VPS et une passerelle WebRTC intégrée au déploiement de l'agent ;
- un direct WebRTC agent → navigateur, avec WebRTC VPS et HLS en secours.
- une PWA mobile installable et un appairage iPhone sécurisé par QR code.
- une timeline glissable avec lecture continue, préchargement des segments et export MP4 multi-segments ;
- un contrôle PTZ ONVIF avec position d'accueil et retour d'erreur réel de l'agent ;
- une surveillance du montage et de l'espace libre, visible depuis le tableau de bord ;
- une reprise FFmpeg supervisée, un inventaire complet périodique et une vérification SHA-256 des transferts.

Les fichiers vidéo ne sont jamais stockés dans MariaDB. La base conserve uniquement leur index. Les segments locaux restent sur l'agent et seul le segment demandé pour la lecture est transféré dans un cache temporaire du VPS.

## Architecture V2

```text
Interface + authentification : Navigateur ── HTTPS ──> CloudNVR VPS
Signalisation WHEP           : Navigateur ── VPS ── connexion sortante ──> Agent
Vidéo prioritaire            : Navigateur <──── WebRTC UDP/TCP ──── Agent <── Caméra
Secours                      : Navigateur <── WebRTC/HLS ── VPS <── Agent
```

Le lecteur reste intégré à l'interface CloudNVR du VPS. Seuls les petits messages de négociation WebRTC transitent par l'API ; les paquets vidéo partent directement de la maison vers le navigateur. L'agent utilise `network_mode: host` afin d'accéder aux caméras du LAN.

Le flux local est indépendant du relais VPS : caméra → FFmpeg agent → MediaMTX agent. Un second relais lit ce restream local pour alimenter le VPS. Une coupure Internet n'arrête donc ni le direct local ni l'enregistrement local. Le chemin VPS reste actif pour le secours WebRTC/HLS et pour les clients qui ne peuvent pas joindre directement la maison.

## Configuration complète des fichiers `.env`

Il existe deux configurations indépendantes :

- `/.env` à la racine configure CloudNVR, MariaDB et MediaMTX sur le VPS ;
- `/deploy/.env` ou `/deploy/agent-only/.env` configure uniquement une machine agent.

Ne copiez pas le `.env` du VPS sur l'agent. Les fichiers `.env` contiennent des
secrets et sont ignorés par Git.

### Variables du VPS — `/.env`

| Variable | Défaut | Obligatoire | Description |
|---|---:|:---:|---|
| `MARIADB_DATABASE` | `cloudnvr` | non | Nom de la base MariaDB créée au premier démarrage. |
| `MARIADB_USER` | `cloudnvr` | non | Utilisateur applicatif MariaDB. |
| `MARIADB_PASSWORD` | aucun | oui | Mot de passe de l'utilisateur applicatif. Utilisez une valeur longue et aléatoire. |
| `MARIADB_ROOT_PASSWORD` | aucun | oui | Mot de passe administrateur MariaDB, utilisé principalement pour l'initialisation et la maintenance. |
| `ADMIN_API_KEY` | aucun | oui | Clé demandée par l'interface et par toutes les routes d'administration. Ce n'est ni le mot de passe MariaDB ni celui de MediaMTX. |
| `CAMERA_ENCRYPTION_KEY` | aucun | oui | Clé AES servant à chiffrer les URL RTSP et mots de passe ONVIF dans MariaDB. Génération : `openssl rand -base64 32`. Elle doit être conservée définitivement. |
| `PUBLIC_URL` | `http://localhost:8080` | production | URL publique complète de l'interface, par exemple `https://cloudnvr.example.com`. Elle sert aux liens PWA, cookies et redirections. |
| `HTTP_PORT` | `8080` | non | Port TCP publié sur l'hôte pour l'interface et l'API. Le conteneur écoute toujours sur `8080`. |
| `CLOUD_RECORDINGS_PATH` | volume `cloud-recordings` | non | Dossier hôte ou volume Docker où sont stockés les enregistrements réalisés par le cloud. Exemple : `/mnt/videos/cloudnvr`. |
| `RECORDING_SEGMENT_TIME` | `1m` | non | Durée d'un segment MP4. Minimum `10s`. Une valeur courte fluidifie la lecture distante mais crée davantage de fichiers. |
| `PLAYBACK_CACHE_TTL` | `6h` | non | Durée de conservation sur le VPS d'un segment temporairement demandé à un agent. Minimum `1m`. |
| `PLAYBACK_CACHE_MAX_BYTES` | `10737418240` | non | Taille maximale du cache de lecture, en octets (10 Gio par défaut). Doit être supérieure ou égale à `AGENT_UPLOAD_MAX_BYTES`. |
| `AGENT_UPLOAD_MAX_BYTES` | `2147483648` | non | Taille maximale acceptée pour un segment envoyé par un agent, en octets (2 Gio par défaut). |
| `CLOUD_MIN_FREE_BYTES` | `5368709120` | non | Espace libre minimal conservé sur le stockage cloud. Les segments finalisés les plus anciens sont supprimés avant de passer sous ce seuil. `0` désactive cette protection. |
| `TZ` | `UTC` | non | Fuseau horaire des conteneurs et de la hiérarchie des dossiers. Exemple : `Indian/Reunion`. |
| `MEDIA_PUBLIC_RTSP_URL` | dérivé de `PUBLIC_URL` sur `8554` | distant | Adresse RTSP du VPS joignable par les agents, par exemple `rtsp://203.0.113.10:8554` ou une adresse VPN. Ne mettez pas `/camera-...` à la fin. |
| `MEDIA_RTSP_PORT` | `8554` | non | Port TCP RTSP publié par MediaMTX sur le VPS. Il reçoit les flux de secours envoyés par les agents. |
| `MEDIA_PUBLISH_USER` | `cloudnvr-agent` | non | Nom d'utilisateur autorisé à publier les flux des agents sur MediaMTX. |
| `MEDIA_PUBLISH_PASSWORD` | `change-media-password` | oui en production | Mot de passe de publication RTSP agent → VPS. Il ne sert pas à ouvrir l'interface et ne correspond pas aux identifiants des caméras. |
| `MEDIA_WEBRTC_HOSTS` | vide | accès externe | IP publique ou DNS annoncé comme candidat ICE par MediaMTX sur le VPS. Plusieurs valeurs peuvent être séparées par des virgules. |
| `MEDIA_WEBRTC_UDP_PORT` | `8189` | non | Port UDP WebRTC publié sur le VPS. C'est le chemin prioritaire du secours VPS. |
| `MEDIA_WEBRTC_TCP_PORT` | `8189` | non | Port TCP WebRTC publié sur le VPS lorsque l'UDP est bloqué. |

Les tailles sont exprimées en octets : `1073741824` = 1 Gio. Les durées utilisent
la syntaxe Go : `30s`, `1m`, `6h`, etc.

### Variables de l'agent — `/deploy/.env`

| Variable | Défaut Compose | Obligatoire | Description |
|---|---:|:---:|---|
| `CLOUD_URL` | aucun | oui | URL de l'interface/API du VPS, sans chemin final. En production, utilisez `https://...`. |
| `SITE_ID` | aucun | oui | Identifiant UUID du site créé dans CloudNVR. Un agent ne peut appartenir qu'à un seul site. |
| `ENROLLMENT_TOKEN` | vide | premier démarrage | Jeton à usage unique affiché lors de la création/réinstallation de l'agent. Il peut être retiré après l'enrôlement réussi. |
| `AGENT_NAME` | `site-agent` | non | Nom technique affiché pour identifier la machine agent. |
| `AGENT_POLL_INTERVAL` | `15s` | non | Fréquence de récupération de la configuration et d'envoi du heartbeat. Utilisez une durée strictement positive. |
| `AGENT_INVENTORY_INTERVAL` | `1m` | non | Fréquence de synchronisation des fichiers ajoutés, modifiés ou supprimés. Minimum `15s`. |
| `AGENT_FULL_INVENTORY_INTERVAL` | `6h` | non | Fréquence du contrôle intégral fichiers ↔ MariaDB. Doit être supérieure ou égale à `AGENT_INVENTORY_INTERVAL`. |
| `AGENT_UPLOAD_MBPS` | `8` | non | Débit maximal en Mbit/s pour envoyer au VPS les segments demandés en lecture. `0` signifie illimité. |
| `AGENT_UPLOAD_RETRIES` | `4` | non | Nombre de tentatives d'envoi d'un segment interrompu. Valeur comprise entre `1` et `20`. |
| `AGENT_MIN_FREE_BYTES` | `2147483648` | non | Réserve minimale du stockage local (2 Gio par défaut). Les plus anciens segments finalisés sont supprimés si nécessaire. `0` désactive cette protection. |
| `AGENT_ALLOW_INSECURE_HTTP` | `false` | non | Autorise un `CLOUD_URL` public en HTTP. Gardez `false` sur Internet. HTTP reste accepté automatiquement pour une IP privée ou `localhost`. |
| `AGENT_RECORDINGS_PATH` | volume `agent-recordings` | non | Dossier de l'hôte monté dans le conteneur pour les enregistrements locaux. Exemple : `/mnt/video/cloudnvr-agent`. |
| `RECORDING_SEGMENT_TIME` | `1m` | non | Durée des MP4 locaux. Minimum `10s`. Peut être différente de celle du VPS. |
| `AGENT_WEBRTC_ENABLED` | `true` | non | Active MediaMTX local et le chemin WebRTC direct agent → navigateur. |
| `AGENT_WEBRTC_HOSTS` | vide | accès direct externe | IP publique ou DNS de la connexion du site, annoncé aux navigateurs. Plusieurs valeurs peuvent être séparées par des virgules. |
| `AGENT_WEBRTC_PORT` | `8189` | non | Port UDP/TCP WebRTC local. Redirigez au minimum `8189/UDP` de la box vers la machine agent. |
| `AGENT_WEBRTC_WORKERS` | `4` | non | Nombre de négociations WebRTC traitées simultanément. Valeur comprise entre `1` et `16`. |
| `TZ` | `UTC` | non | Fuseau utilisé pour les dates et le classement des segments. Utilisez le même fuseau sur le VPS et l'agent. |

### Variables internes aux conteneurs

Le Compose configure aussi les variables suivantes. Il n'est normalement pas
nécessaire de les mettre dans `.env`, car leurs adresses correspondent aux noms
et volumes internes Docker :

| Service | Variables internes | Rôle |
|---|---|---|
| Cloud | `HTTP_ADDR`, `DATABASE_DSN`, `WEB_UPSTREAM` | Adresse d'écoute, connexion MariaDB et proxy vers l'interface web. |
| Cloud/Media | `MEDIA_WEBRTC_UPSTREAM`, `MEDIA_HLS_UPSTREAM`, `MEDIA_API_URL`, `MEDIA_INTERNAL_RTSP_URL` | Communications privées entre l'API et MediaMTX du VPS. |
| Cloud/Stockage | `RECORDINGS_PATH`, `CLOUD_STORAGE_ID_FILE` | Chemins internes du volume vidéo et de son identité persistante. |
| Web | `NEXT_PUBLIC_SITE_URL` | URL publique transmise au build/serveur web ; le Compose reprend `PUBLIC_URL`. |
| Agent/Media | `AGENT_MEDIA_RTSP_URL`, `AGENT_MEDIA_WEBRTC_URL` | RTSP local `127.0.0.1:8555` et signalisation locale `127.0.0.1:8890`. Ces ports ne doivent pas être exposés sur Internet. |
| Agent/État | `AGENT_STATE_FILE`, `AGENT_CONFIG_CACHE_FILE`, `AGENT_INVENTORY_STATE_FILE`, `AGENT_STORAGE_ID_FILE`, `RECORDINGS_PATH` | Identité de l'agent, configuration hors ligne, inventaire et contrôle du montage. Ces chemins restent dans les volumes Docker. |
| MediaMTX | `MTX_WEBRTCADDITIONALHOSTS`, `MTX_WEBRTCLOCALUDPADDRESS`, `MTX_WEBRTCLOCALTCPADDRESS` | Valeurs MediaMTX générées depuis les variables WebRTC publiques précédentes. |

Points importants :

- changer `CAMERA_ENCRYPTION_KEY` après avoir ajouté des caméras rend leurs secrets existants illisibles ;
- changer `MARIADB_PASSWORD` dans `.env` ne change pas automatiquement le mot de passe d'un volume MariaDB déjà initialisé ;
- `CLOUD_RECORDINGS_PATH` et `AGENT_RECORDINGS_PATH` doivent être accessibles à l'UID `10001` ;
- changer `MEDIA_PUBLISH_PASSWORD` nécessite de recréer le conteneur cloud, puis l'agent récupère automatiquement ses nouvelles URL de publication ;
- supprimer le volume `agent-state` oblige à générer un nouveau `ENROLLMENT_TOKEN`.

## Démarrage du cloud

Prérequis : Docker avec Docker Compose.

```bash
cp .env.example .env
openssl rand -base64 32
```

Reporter la clé générée dans `CAMERA_ENCRYPTION_KEY`, choisir des mots de passe forts, puis lancer :

```bash
docker compose up --build -d
curl http://localhost:8080/health
```

Le clone complet construit tout localement. Après publication des paquets
GHCR, `docker compose pull && docker compose up -d` permet aussi d'utiliser les
images précompilées.

Ouvrir ensuite `PUBLIC_URL` dans le navigateur. L'interface demande la valeur de `ADMIN_API_KEY` et la conserve uniquement pendant la session du navigateur.

Si le port 8080 est déjà occupé, modifier `HTTP_PORT` et `PUBLIC_URL` dans `.env`.

Pour un déploiement OVH, placer impérativement l'API derrière HTTPS (Caddy, Traefik ou le load balancer OVH) et définir `PUBLIC_URL` avec l'URL publique HTTPS.

Renseigner également `MEDIA_PUBLIC_RTSP_URL` avec l'IP ou le nom DNS du VPS, et `MEDIA_WEBRTC_HOSTS` avec son IP publique ou son DNS. Ouvrir le port TCP `8554` pour les agents ainsi que les ports UDP et TCP `8189` pour WebRTC. Le lecteur HLS reste disponible si WebRTC est bloqué.

Pour un agent distant, protégez le transport RTSP entre le site et le VPS avec un VPN (WireGuard ou Tailscale), ou limitez strictement le port `8554` aux IP autorisées. La documentation officielle MediaMTX recommande un chiffrement ou un VPN lorsque des identifiants RTSP traversent le réseau.

## Créer un site

Toutes les routes d'administration attendent `Authorization: Bearer $ADMIN_API_KEY`.

```bash
curl -X POST http://localhost:8080/api/sites \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Maison",
    "location": "La Réunion",
    "default_recording_mode": "hybrid"
  }'
```

La réponse contient `SITE_ID` et `ENROLLMENT_TOKEN`. Ce jeton d'enrôlement est à usage unique. Le jeton d'agent généré après l'enrôlement est conservé dans le volume `agent-state`.

## Installer l'agent sur le site

Sur la machine locale, copier le modèle complet dans le dossier `deploy` :

```bash
cp deploy/.env.example deploy/.env
# compléter CLOUD_URL, SITE_ID, ENROLLMENT_TOKEN et AGENT_RECORDINGS_PATH
```

Puis lancer :

```bash
docker compose -f deploy/agent-compose.yml up --build -d
```

Le fichier Compose V2 lance deux conteneurs sur la machine locale : `agent` et `agent-media`. Ils forment ensemble l'agent CloudNVR V2. `agent-media` écoute le RTSP et la signalisation uniquement sur `127.0.0.1`; seul le transport WebRTC `8189` écoute sur le réseau.

Pour un accès direct depuis Internet, rediriger sur la box le port `8189/UDP` vers la machine agent. La redirection `8189/TCP` est recommandée comme solution de repli. Ne jamais exposer les ports RTSP locaux `8555` ou HTTP `8890`. En cas de CGNAT ou de redirection impossible, utiliser le bouton **Secours VPS** ; le futur ajout d'un serveur TURN permettra aussi de conserver un chemin direct relayé.

Après le premier démarrage, `ENROLLMENT_TOKEN` peut être retiré du fichier : l'agent utilise son propre jeton stocké dans son volume privé.

L'agent conserve aussi une copie protégée de la dernière configuration. Après un redémarrage sans Internet, il reprend donc immédiatement les caméras et les enregistrements locaux. Le moteur d'enregistrement est indépendant du relais en direct : une coupure du VPS n'interrompt pas FFmpeg local. Au retour du réseau, un inventaire complet remet MariaDB en cohérence.

Le dossier d'enregistrement reçoit un identifiant persistant. Si un montage QNAP disparaît, l'agent met les enregistreurs en pause et ne vide pas la timeline. Ils redémarrent automatiquement lorsque le même montage revient.

Toutes les variables, limites et valeurs par défaut sont détaillées dans
**Configuration complète des fichiers `.env`** plus haut.

### Installer seulement l'agent

Il n'est pas nécessaire de télécharger ni de compiler tout CloudNVR sur le
site distant. L'installation autonome se trouve dans `deploy/agent-only` :

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/Razer97427/CloudNVR.git
cd CloudNVR
git sparse-checkout set deploy/agent-only
cd deploy/agent-only
cp .env.example .env
# compléter le fichier .env avec les valeurs données par CloudNVR
docker compose pull
docker compose up -d
```

L'image agent est publiée pour `amd64`, `arm64` et `arm/v7`. Le paquet GHCR
`cloudnvr-agent` doit être rendu public dans les réglages GitHub du dépôt pour
que les utilisateurs n'aient pas besoin d'un jeton GitHub.

Le trafic API, la configuration et les segments utilisent `CLOUD_URL` en HTTPS avec un jeton propre à l'agent. Pour le relais RTSP en direct, utiliser un VPN WireGuard/Tailscale ou une URL `rtsps://` configurée côté MediaMTX : l'authentification RTSP seule ne chiffre pas la vidéo.

Si l'agent doit être réinstallé, générer un nouveau jeton à usage unique :

```bash
curl -X POST http://localhost:8080/api/sites/$SITE_ID/enrollment-token \
  -H "Authorization: Bearer $ADMIN_API_KEY"
```

La même action est disponible dans l'interface, onglet **Sites**, avec le bouton **Réinstaller l’agent**. L'agent actuel reste connecté tant que le nouvel agent ne s'est pas enrôlé.

## Ajouter une caméra

L'adresse est celle que l'agent peut atteindre sur le réseau local :

```bash
curl -X POST http://localhost:8080/api/sites/$SITE_ID/cameras \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Entrée",
    "stream_url": "rtsp://admin:secret@192.168.1.50:554/stream1",
    "recording_mode": "hybrid",
    "local_retention_days": 7,
    "cloud_retention_days": 30,
    "transfer_policy": "events_and_manual"
  }'
```

L'API d'administration masque ensuite `stream_url`. Seul l'agent authentifié du site reçoit sa valeur déchiffrée.

Depuis l'onglet **Caméras**, le bouton **Modifier** permet de changer le nom, l'adresse RTSP, le mode et les rétentions. Laisser le champ de nouvelle adresse vide conserve l'adresse chiffrée actuelle.

Chaque caméra dispose également d'un mode d'accès réseau :

- `agent` : l'agent du site accède à la caméra locale ;
- `direct` : le serveur CloudNVR accède lui-même à la caméra, sans agent. Ce mode nécessite que le serveur soit sur le même LAN ou relié au réseau de la caméra par VPN.

## Modifier une politique de sauvegarde

```bash
curl -X PUT http://localhost:8080/api/cameras/$CAMERA_ID/policy \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "recording_mode": "manual",
    "local_retention_days": 7,
    "cloud_retention_days": 30,
    "transfer_policy": "manual",
    "enabled": true
  }'
```

Signification des modes :

- `local` : conservation sur l'agent ;
- `cloud` : envoi et conservation dans le cloud ;
- `hybrid` : tampon local et transfert selon `transfer_policy` ;
- `manual` : pas d'enregistrement permanent, démarrage demandé par l'utilisateur ;
- `disabled` : direct uniquement, sans sauvegarde.

## Routes disponibles

| Méthode | Route | Accès |
|---|---|---|
| `GET` | `/health` | public |
| `POST`, `GET` | `/api/sites` | administrateur |
| `POST` | `/api/sites/{siteID}/enrollment-token` | administrateur |
| `POST`, `GET` | `/api/sites/{siteID}/cameras` | administrateur |
| `PUT` | `/api/cameras/{cameraID}` | administrateur |
| `PUT` | `/api/cameras/{cameraID}/policy` | administrateur |
| `GET` | `/api/cameras/{cameraID}/stream` | administrateur |
| `POST` | `/api/mobile/pairings` | administrateur |
| `POST` | `/api/mobile/claim` | code d'appairage |
| `GET`, `DELETE` | `/api/mobile/devices[/{deviceID}]` | administrateur |
| `GET`, `POST` | `/api/session`, `/api/session/logout` | administrateur ou appareil appairé |
| `POST` | `/api/agent/enroll` | jeton d'enrôlement |
| `POST` | `/api/agent/heartbeat` | agent |
| `GET` | `/api/agent/config` | agent |
| `GET`, `POST` | `/api/agent/webrtc-requests[/{requestID}]` | agent |

## Lecture en direct

Dans l'onglet **Caméras**, cliquer sur **Lire la caméra**. Pour une caméra reliée à un agent V2 en ligne, **Direct maison** est sélectionné par défaut. Le bouton **Secours VPS** force le WebRTC du VPS et **HLS secours** fonctionne sur les réseaux qui bloquent WebRTC. Si l'agent local ne répond pas lors de l'ouverture du lecteur, CloudNVR redirige automatiquement la première tentative vers le WebRTC du VPS.

Les ports `8554` et `8189` du VPS doivent rester ouverts tant que le chemin de secours est utilisé : `8554` reçoit le restream de l'agent et `8189` transporte son WebRTC de secours. Le port `8189` ouvert sur la box est un port différent, dirigé vers l'agent local.

Le relais vidéo copie le flux sans le réencoder afin de préserver les ressources du VPS. Configurez de préférence la caméra ou son sous-flux en H.264 ; un flux H.265 n'est pas pris en charge par tous les navigateurs.

## Enregistrements, intégrité et stockage

Les segments finalisés sont rangés sous
`nom-camera__id/année/mois/jour/heure/minute-seconde.mp4`. La timeline est
reconstruite depuis l'inventaire réel : lors d'une synchronisation complète,
une ligne MariaDB est supprimée si le fichier correspondant n'existe plus.

Les nouveaux segments reçoivent progressivement un checksum SHA-256 conservé
à côté du MP4. Un segment envoyé par un agent est refusé si son contenu ne
correspond pas à ce checksum. Le calcul est limité à quelques fichiers par
passage afin de ne pas surcharger un NAS.

L'agent vérifie également l'identité du point de montage. Si le NFS disparaît,
les enregistreurs sont mis en pause au lieu d'écrire silencieusement sur le
disque système. `AGENT_MIN_FREE_BYTES` et `CLOUD_MIN_FREE_BYTES` conservent une
réserve en supprimant les fichiers finalisés les plus anciens ; l'inventaire
suivant nettoie automatiquement MariaDB.

Dans **Enregistrements**, le lecteur prépare uniquement le segment demandé et
précharge ses voisins. L'export simple découpe le segment courant. L'export de
plage assemble les segments d'une même caméra dans un MP4 de deux heures
maximum, sans transférer le reste de la journée.

## Installer l'application sur iPhone

Dans l'administration, cliquer sur **App mobile** puis scanner le QR code avec l'appareil photo de l'iPhone. Le code :

- expire après 10 minutes ;
- ne peut être utilisé qu'une seule fois ;
- ne contient jamais la clé `ADMIN_API_KEY`.

Après autorisation, utiliser dans Safari **Partager → Sur l'écran d'accueil**. L'accès de l'appareil est conservé pendant 180 jours dans un cookie sécurisé et inaccessible au JavaScript. Les appareils autorisés sont visibles et révocables depuis la même fenêtre **App mobile**.

Sur un VPS, `PUBLIC_URL` doit impérativement contenir l'adresse HTTPS réellement accessible depuis l'iPhone. L'installation PWA et le cookie `Secure` nécessitent HTTPS en production.

## Développement

```bash
make test
make build
```

La migration initiale se trouve dans `migrations/001_initial.sql`. Les migrations
complémentaires sont appliquées automatiquement au démarrage de l'API, y compris
sur un volume MariaDB existant.
