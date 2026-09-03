# Agent CloudNVR seul

Ce dossier suffit pour installer un agent sur un site distant. Il utilise
l'image précompilée publiée sur GHCR : aucun compilateur Go ou clone complet
du projet n'est nécessaire.

```bash
cp .env.example .env
# compléter CLOUD_URL, SITE_ID, ENROLLMENT_TOKEN et le stockage
docker compose pull
docker compose up -d
docker compose logs -f agent
```

Le jeton d'enrôlement est à usage unique. Après le premier démarrage réussi,
il peut être supprimé du `.env`; le jeton permanent reste dans `agent-state`.

Le chemin `AGENT_RECORDINGS_PATH` doit exister et être inscriptible par
l'UID 10001. Pour un NFS, montez-le d'abord sur l'hôte puis démarrez Compose.
L'agent refuse d'enregistrer si le montage identifié disparaît, et conserve au
moins `AGENT_MIN_FREE_BYTES` en supprimant d'abord les segments les plus anciens.

Seul le port `8189/udp` doit être redirigé depuis Internet pour le WebRTC direct
(`8189/tcp` est un secours utile). Les ports locaux `8555` et `8890` restent
réservés à l'hôte et ne doivent pas être exposés.

Pour récupérer uniquement ce dossier depuis GitHub :

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/Razer97427/CloudNVR.git
cd CloudNVR
git sparse-checkout set deploy/agent-only
cd deploy/agent-only
```
