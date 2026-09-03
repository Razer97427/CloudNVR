'use client';

import { useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode, KeyboardEvent as ReactKeyboardEvent } from 'react';
import QRCode from 'qrcode';

type RecordingMode = 'local' | 'cloud' | 'hybrid' | 'manual' | 'disabled';
type TransferPolicy = 'all' | 'events' | 'manual' | 'events_and_manual' | 'none';
type AccessMode = 'agent' | 'direct';

type Site = {
  id: string;
  name: string;
  stream_url: string;
  access_mode: AccessMode;
  location: string;
  default_recording_mode: RecordingMode;
  camera_count: number;
  agent_status: 'online' | 'offline' | 'not_enrolled';
  storage_ok: boolean;
  storage_total_bytes: number;
  storage_free_bytes: number;
  recording_workers: number;
  relay_workers: number;
  agent_health_error?: string;
  created_at: string;
};

type Camera = {
  id: string;
  site_id: string;
  name: string;
  stream_url: string;
  access_mode: AccessMode;
  recording_mode: RecordingMode;
  local_retention_days: number;
  cloud_retention_days: number;
  transfer_policy: TransferPolicy;
  enabled: boolean;
  manual_recording: boolean;
  ptz_enabled: boolean;
  ptz_endpoint?: string;
  ptz_username?: string;
};

type StreamInfo = {
  webrtc_url: string;
  cloud_webrtc_url: string;
  agent_webrtc_url?: string;
  hls_url: string;
  webrtc_mode: 'agent_direct' | 'cloud';
};
type LiveProtocol = 'webrtc' | 'cloud_webrtc' | 'hls';
type PTZAction = 'move' | 'set_home' | 'goto_home';
type PTZControl = (camera: Camera, pan: number, tilt: number, zoom: number, action?: PTZAction) => Promise<void>;
type PlaybackPreparation = { status: 'queued' | 'uploading' | 'ready' | 'error'; playback_url?: string; error?: string };
type Recording = {
  id: string; camera_id: string; camera_name: string; site_id: string; site_name: string;
  source: 'cloud' | 'agent'; started_at: string; ended_at?: string; size_bytes: number; event_type: string; playback_url?: string;
};
type DeviceSession = { id: string; name: string; last_seen_at: string; expires_at: string; created_at: string };
type MobilePairing = { pairing_url: string; expires_at: string };

type Enrollment = { site: Site; enrollment_token: string; agent_environment: Record<string, string> };
type View = 'dashboard' | 'live' | 'sites' | 'cameras' | 'recordings';

const modeLabels: Record<RecordingMode, string> = {
  local: 'Local', cloud: 'Cloud', hybrid: 'Hybride', manual: 'Manuel', disabled: 'Désactivé',
};
const transferLabels: Record<TransferPolicy, string> = {
  all: 'Tout transférer', events: 'Événements', manual: 'Manuel', events_and_manual: 'Événements + manuel', none: 'Aucun transfert',
};
const DEVICE_SESSION = '__paired_device__';

function configuredONVIFPort(endpoint?: string) {
  try {
    const url = new URL(endpoint || '');
    if (url.port) return Number(url.port);
    return url.protocol === 'https:' ? 443 : 80;
  } catch { return 80; }
}

function buildONVIFEndpoint(form: FormData) {
  const enabled = form.get('ptz_enabled') === 'on';
  const explicit = String(form.get('ptz_endpoint') || '').trim();
  if (!enabled && !explicit) return '';
  const port = Math.min(65535, Math.max(1, Number(form.get('ptz_port')) || 80));
  try {
    const url = new URL(explicit || String(form.get('stream_url') || ''));
    url.protocol = explicit ? url.protocol : 'http:';
    url.username = '';
    url.password = '';
    url.port = String(port);
    if (!explicit) url.pathname = '/onvif/device_service';
    url.search = '';
    url.hash = '';
    return url.toString();
  } catch { return explicit; }
}

export default function Dashboard() {
  const [apiKey, setApiKey] = useState<string | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [selectedSiteID, setSelectedSiteID] = useState('');
  const [view, setView] = useState<View>('dashboard');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [siteModal, setSiteModal] = useState(false);
  const [cameraModal, setCameraModal] = useState(false);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [authMode, setAuthMode] = useState<'admin_key' | 'paired_device'>('admin_key');
  const [pairingCode, setPairingCode] = useState<string | null>(null);
  const [pairingComplete, setPairingComplete] = useState(false);
  const [mobileModal, setMobileModal] = useState(false);
  const [mobileLoading, setMobileLoading] = useState(false);
  const [mobileError, setMobileError] = useState('');
  const [mobilePairing, setMobilePairing] = useState<(MobilePairing & { qr: string }) | null>(null);
  const [devices, setDevices] = useState<DeviceSession[]>([]);
  const preparedRecordings = useRef(new Map<string, Promise<string>>());

  useEffect(() => {
    if ('serviceWorker' in navigator) void navigator.serviceWorker.register('/sw.js').catch(() => undefined);
    const pair = new URLSearchParams(window.location.search).get('pair');
    if (pair) {
      setPairingCode(pair);
      setApiKey('');
      setLoading(false);
      return;
    }
    const saved = sessionStorage.getItem('cloudnvr_admin_key');
    if (saved) {
      setApiKey(saved);
      setAuthMode('admin_key');
      void refresh(saved);
      return;
    }
    void fetch('/api/session').then(async response => {
      if (!response.ok) throw new Error('no device session');
      setApiKey(DEVICE_SESSION);
      setAuthMode('paired_device');
      await refresh(DEVICE_SESSION);
    }).catch(() => {
      setApiKey('');
      setLoading(false);
    });
  }, []);

  async function request<T>(path: string, options: RequestInit = {}, key = apiKey): Promise<T> {
    const authorization = key && key !== DEVICE_SESSION ? { Authorization: `Bearer ${key}` } : {};
    const response = await fetch(path, {
      ...options,
      headers: { 'Content-Type': 'application/json', ...authorization, ...options.headers },
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({ error: 'Réponse invalide du serveur' }));
      throw new Error(body.error || `Erreur ${response.status}`);
    }
    if (response.status === 204) return undefined as T;
    return response.json();
  }

  async function refresh(key = apiKey, preferredSiteID = selectedSiteID) {
    if (!key) return;
    setLoading(true);
    setError('');
    try {
      const siteResponse = await request<{ sites: Site[] }>('/api/sites', {}, key);
      setSites(siteResponse.sites || []);
      const currentSiteID = preferredSiteID || siteResponse.sites?.[0]?.id || '';
      setSelectedSiteID(currentSiteID);
      const cameraGroups = await Promise.all((siteResponse.sites || []).map((site) =>
        request<{ cameras: Camera[] }>(`/api/sites/${site.id}/cameras`, {}, key),
      ));
      setCameras(cameraGroups.flatMap((group) => group.cameras || []));
      const recordingResponse = await request<{ recordings: Recording[] }>('/api/recordings?limit=5000', {}, key);
      setRecordings(recordingResponse.recordings || []);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Connexion impossible';
      setError(message);
      if (message.toLowerCase().includes('token') || message.toLowerCase().includes('session')) void logout();
    } finally {
      setLoading(false);
    }
  }

  function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const key = String(new FormData(event.currentTarget).get('api_key') || '').trim();
    if (!key) return;
    sessionStorage.setItem('cloudnvr_admin_key', key);
    setApiKey(key);
    setAuthMode('admin_key');
    void refresh(key);
  }

  async function logout() {
    if (authMode === 'paired_device') {
      await request('/api/session/logout', { method: 'POST' }).catch(() => undefined);
    }
    sessionStorage.removeItem('cloudnvr_admin_key');
    setApiKey('');
    setSites([]);
    setCameras([]);
  }

  async function claimPairing(deviceName: string) {
    const response = await fetch('/api/mobile/claim', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: pairingCode, device_name: deviceName }),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({ error: 'Appairage impossible' }));
      throw new Error(body.error || 'Appairage impossible');
    }
    window.history.replaceState({}, '', '/');
    setPairingCode(null);
    setPairingComplete(true);
    setApiKey(DEVICE_SESSION);
    setAuthMode('paired_device');
  }

  async function enterPairedApp() {
    setPairingComplete(false);
    await refresh(DEVICE_SESSION);
  }

  async function openMobilePairing() {
    setMobileModal(true);
    setMobileLoading(true);
    setMobileError('');
    try {
      const [pairing, deviceList] = await Promise.all([
        request<MobilePairing>('/api/mobile/pairings', { method: 'POST' }),
        request<{ devices: DeviceSession[] }>('/api/mobile/devices'),
      ]);
      const qr = await QRCode.toDataURL(pairing.pairing_url, { width: 300, margin: 1, color: { dark: '#102039', light: '#ffffff' } });
      setMobilePairing({ ...pairing, qr });
      setDevices(deviceList.devices || []);
    } catch (err) {
      setMobileError(err instanceof Error ? err.message : 'QR code impossible à générer');
    } finally {
      setMobileLoading(false);
    }
  }

  async function revokeDevice(deviceID: string) {
    try {
      await request(`/api/mobile/devices/${deviceID}`, { method: 'DELETE' });
      setDevices(current => current.filter(device => device.id !== deviceID));
    } catch (err) {
      setMobileError(err instanceof Error ? err.message : 'Révocation impossible');
    }
  }

  async function createSite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      const created = await request<Enrollment>('/api/sites', { method: 'POST', body: JSON.stringify(data) });
      setSiteModal(false);
      setEnrollment(created);
      setSelectedSiteID(created.site.id);
      await refresh(apiKey, created.site.id);
    } catch (err) { setError(err instanceof Error ? err.message : 'Création impossible'); }
  }

  async function createCamera(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedSiteID) return;
    setError('');
    const form = new FormData(event.currentTarget);
    const payload = {
      name: form.get('name'), stream_url: form.get('stream_url'), access_mode: form.get('access_mode'), recording_mode: form.get('recording_mode'),
      transfer_policy: form.get('transfer_policy'), local_retention_days: Number(form.get('local_retention_days')),
      cloud_retention_days: Number(form.get('cloud_retention_days')),
      ptz_enabled: form.get('ptz_enabled') === 'on', ptz_endpoint: buildONVIFEndpoint(form),
      ptz_username: String(form.get('ptz_username') || '').trim(), ptz_password: String(form.get('ptz_password') || ''),
    };
    try {
      await request(`/api/sites/${selectedSiteID}/cameras`, { method: 'POST', body: JSON.stringify(payload) });
      setCameraModal(false);
      setNotice('Caméra ajoutée. L’agent la recevra à sa prochaine synchronisation.');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Ajout impossible'); }
  }

  async function updateCamera(event: FormEvent<HTMLFormElement>, camera: Camera) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const payload = {
      name: String(form.get('name') || '').trim(), stream_url: String(form.get('stream_url') || '').trim(),
      access_mode: form.get('access_mode'), recording_mode: form.get('recording_mode'), transfer_policy: form.get('transfer_policy'),
      local_retention_days: Number(form.get('local_retention_days')), cloud_retention_days: Number(form.get('cloud_retention_days')),
      enabled: form.get('enabled') === 'on',
      ptz_enabled: form.get('ptz_enabled') === 'on', ptz_endpoint: buildONVIFEndpoint(form),
      ptz_username: String(form.get('ptz_username') || '').trim(), ptz_password: String(form.get('ptz_password') || ''),
    };
    try {
      await request(`/api/cameras/${camera.id}`, { method: 'PUT', body: JSON.stringify(payload) });
      setNotice(`Configuration de « ${payload.name} » enregistrée. L’agent recevra la modification automatiquement.`);
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Mise à jour impossible'); }
  }

  async function prepareAgent(site: Site) {
    setError('');
    try {
      const result = await request<{ enrollment_token: string; agent_environment: Record<string, string> }>(`/api/sites/${site.id}/enrollment-token`, { method: 'POST' });
      setEnrollment({ site, enrollment_token: result.enrollment_token, agent_environment: result.agent_environment });
    } catch (err) { setError(err instanceof Error ? err.message : 'Configuration impossible'); }
  }

  async function getStream(cameraID: string) {
    return request<StreamInfo>(`/api/cameras/${cameraID}/stream`);
  }

  async function setManualRecording(camera: Camera) {
    try {
      await request(`/api/cameras/${camera.id}/recording`, { method: 'POST', body: JSON.stringify({ active: !camera.manual_recording }) });
      setNotice(camera.manual_recording ? 'Enregistrement manuel arrêté.' : 'Enregistrement manuel démarré.');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Commande impossible'); }
  }

  async function controlPTZ(camera: Camera, pan: number, tilt: number, zoom: number, action: PTZAction = 'move') {
    try {
      await request(`/api/cameras/${camera.id}/ptz`, { method: 'POST', body: JSON.stringify({ action, pan, tilt, zoom }) });
      if (action === 'set_home') setNotice('Commande envoyée : la position actuelle sera enregistrée comme accueil par la caméra.');
      if (action === 'goto_home') setNotice('Commande « aller à l’accueil » envoyée.');
    } catch (err) { setError(err instanceof Error ? err.message : 'Commande PTZ impossible'); throw err; }
  }

  async function prepareLocalRecording(recordingID: string) {
    const existing = preparedRecordings.current.get(recordingID);
    if (existing) return existing;
    const preparation = (async () => {
      let status = await request<PlaybackPreparation>(`/api/recordings/${recordingID}/prepare`, { method: 'POST' });
      for (let attempt = 0; attempt < 90; attempt += 1) {
        if (status.status === 'ready' && status.playback_url) return status.playback_url;
        if (status.status === 'error') throw new Error(status.error || 'Enregistrement indisponible sur l’agent');
        await new Promise(resolve => window.setTimeout(resolve, 1000));
        status = await request<PlaybackPreparation>(`/api/recordings/${recordingID}/prepare`);
      }
      throw new Error('L’agent met trop de temps à envoyer cet enregistrement.');
    })();
    preparedRecordings.current.set(recordingID, preparation);
    try { return await preparation; }
    catch (err) { preparedRecordings.current.delete(recordingID); throw err; }
  }

  async function exportRecording(recording: Recording, from: number, to: number) {
    setNotice('Préparation de l’extrait vidéo…');
    try {
      if (recording.source === 'agent') await prepareLocalRecording(recording.id);
      const headers: HeadersInit = apiKey && apiKey !== DEVICE_SESSION ? { Authorization: `Bearer ${apiKey}` } : {};
      const response = await fetch(`/api/recordings/${recording.id}/export?from=${from.toFixed(3)}&to=${to.toFixed(3)}`, { method: 'POST', headers });
      if (!response.ok) {
        const problem = await response.json().catch(() => ({ error: 'Export impossible' }));
        throw new Error(problem.error || 'Export impossible');
      }
      const blobURL = URL.createObjectURL(await response.blob());
      const disposition = response.headers.get('Content-Disposition') || '';
      const filename = disposition.match(/filename="([^"]+)"/)?.[1] || `cloudnvr-${recording.camera_name}.mp4`;
      const link = document.createElement('a'); link.href = blobURL; link.download = filename; link.click();
      window.setTimeout(() => URL.revokeObjectURL(blobURL), 30_000);
      setNotice(`Extrait de ${Math.round(to - from)} secondes exporté.`);
    } catch (err) { setError(err instanceof Error ? err.message : 'Export impossible'); throw err; }
  }

  async function exportRecordingRange(cameraID: string, from: Date, to: Date) {
    try {
      if (!cameraID || to <= from || to.getTime() - from.getTime() > 2 * 60 * 60 * 1000) throw new Error('Choisissez une plage valide de 2 heures maximum.');
      setNotice('Préparation des segments de la plage…');
      const overlapping = recordings
      .filter(recording => recording.camera_id === cameraID
        && new Date(recording.started_at).getTime() < to.getTime()
        && new Date(recording.ended_at || new Date(new Date(recording.started_at).getTime() + 60_000)).getTime() > from.getTime());
      if (!overlapping.length) throw new Error('Aucun enregistrement dans cette plage.');
      const local = overlapping.filter(recording => recording.source === 'agent');
      for (let offset = 0; offset < local.length; offset += 3) {
        await Promise.all(local.slice(offset, offset + 3).map(recording => prepareLocalRecording(recording.id)));
      }
      const headers: HeadersInit = { 'Content-Type': 'application/json', ...(apiKey && apiKey !== DEVICE_SESSION ? { Authorization: `Bearer ${apiKey}` } : {}) };
      const response = await fetch('/api/recordings/export-range', { method: 'POST', headers, body: JSON.stringify({ camera_id: cameraID, from: from.toISOString(), to: to.toISOString() }) });
      if (!response.ok) {
        const problem = await response.json().catch(() => ({ error: 'Export impossible' }));
        throw new Error(problem.error || 'Export impossible');
      }
      const blobURL = URL.createObjectURL(await response.blob());
      const disposition = response.headers.get('Content-Disposition') || '';
      const filename = disposition.match(/filename="([^"]+)"/)?.[1] || 'cloudnvr-extrait.mp4';
      const link = document.createElement('a'); link.href = blobURL; link.download = filename; link.click();
      window.setTimeout(() => URL.revokeObjectURL(blobURL), 30_000);
      setNotice(`Plage de ${Math.round((to.getTime() - from.getTime()) / 60_000)} minutes exportée.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export impossible');
      throw err;
    }
  }

  const selectedSite = sites.find((site) => site.id === selectedSiteID);
  const selectedCameras = cameras.filter((camera) => camera.site_id === selectedSiteID);
  const onlineSites = sites.filter((site) => site.agent_status === 'online').length;
  const onlineAgents = sites.filter((site) => site.agent_status === 'online').length;
  const title = view === 'dashboard' ? 'Vue d’ensemble' : view === 'live' ? 'Vue en direct' : view === 'sites' ? 'Sites' : view === 'cameras' ? 'Configuration des caméras' : 'Enregistrements';

  if (pairingCode) return <PairingScreen onClaim={claimPairing} />;
  if (pairingComplete) return <InstallSuccess onOpen={() => void enterPairedApp()} />;
  if (apiKey === null || loading && !sites.length) return <LoadingScreen />;
  if (!apiKey) return <Login onSubmit={login} error={error} />;

  return (
    <main className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark"><span /></span><span>CloudNVR</span></div>
        <nav aria-label="Navigation principale">
          <NavItem icon="⌂" label="Vue d’ensemble" active={view === 'dashboard'} onClick={() => setView('dashboard')} />
          <NavItem icon="◉" label="Vue en direct" active={view === 'live'} onClick={() => setView('live')} />
          <NavItem icon="◇" label="Sites" active={view === 'sites'} onClick={() => setView('sites')} />
          <NavItem icon="⚙" label="Caméras" active={view === 'cameras'} onClick={() => setView('cameras')} />
          <NavItem icon="▣" label="Enregistrements" active={view === 'recordings'} onClick={() => setView('recordings')} />
        </nav>
        <div className="sidebar-bottom">
          <div className="cloud-state"><span className="pulse" /><div><strong>Cloud opérationnel</strong><small>{sites.length} site{sites.length > 1 ? 's' : ''} configuré{sites.length > 1 ? 's' : ''}</small></div></div>
          <button className="profile" onClick={() => void logout()}><span>{authMode === 'paired_device' ? 'MB' : 'AD'}</span><div><strong>{authMode === 'paired_device' ? 'Appareil mobile' : 'Administrateur'}</strong><small>Se déconnecter</small></div></button>
        </div>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <div><p className="eyebrow">CloudNVR · Administration</p><h1>{title}</h1></div>
          <div className="topbar-actions"><button className="secondary-button mobile-button" onClick={() => void openMobilePairing()}><span>▦</span> App mobile</button>{(view === 'dashboard' || view === 'sites' || view === 'cameras') && <button className="primary-button" onClick={() => view === 'cameras' ? setCameraModal(true) : setSiteModal(true)} disabled={view === 'cameras' && !sites.length}><span>＋</span>{view === 'cameras' ? 'Ajouter une caméra' : 'Nouveau site'}</button>}</div>
        </header>

        <div className="content">
          {error && <div className="alert error-alert"><span>!</span>{error}<button onClick={() => setError('')}>×</button></div>}
          {notice && <div className="alert success-alert"><span>✓</span>{notice}<button onClick={() => setNotice('')}>×</button></div>}
          {view === 'dashboard' && <DashboardView sites={sites} cameras={cameras} onlineSites={onlineSites} onlineAgents={onlineAgents} onCreate={() => setSiteModal(true)} onOpenSites={() => setView('sites')} />}
          {view === 'live' && <LiveView sites={sites} cameras={cameras} selectedSiteID={selectedSiteID} onSiteChange={setSelectedSiteID} onGetStream={getStream} onPTZ={controlPTZ} onConfigure={() => setView('cameras')} />}
          {view === 'sites' && <SitesView sites={sites} selected={selectedSiteID} onSelect={(id) => { setSelectedSiteID(id); setView('cameras'); }} onDeploy={prepareAgent} onCreate={() => setSiteModal(true)} />}
          {view === 'cameras' && <CamerasView sites={sites} selectedSite={selectedSite} selectedSiteID={selectedSiteID} cameras={selectedCameras} onSiteChange={setSelectedSiteID} onCreate={() => setCameraModal(true)} onUpdate={updateCamera} onGetStream={getStream} onManualRecording={setManualRecording} onPTZ={controlPTZ} />}
          {view === 'recordings' && <RecordingsView recordings={recordings} sites={sites} cameras={cameras} onPrepare={prepareLocalRecording} onExport={exportRecording} onExportRange={exportRecordingRange} />}
          {loading && sites.length > 0 && <div className="syncing">Synchronisation…</div>}
        </div>
      </section>

      {siteModal && <SiteModal onClose={() => setSiteModal(false)} onSubmit={createSite} />}
      {cameraModal && <CameraModal sites={sites} siteID={selectedSiteID} onSiteChange={setSelectedSiteID} onClose={() => setCameraModal(false)} onSubmit={createCamera} />}
      {enrollment && <EnrollmentModal enrollment={enrollment} onClose={() => setEnrollment(null)} />}
      {mobileModal && <MobileModal pairing={mobilePairing} devices={devices} loading={mobileLoading} error={mobileError} onRegenerate={() => void openMobilePairing()} onRevoke={id => void revokeDevice(id)} onClose={() => setMobileModal(false)} />}
    </main>
  );
}

function NavItem({ icon, label, active, onClick }: { icon: string; label: string; active: boolean; onClick: () => void }) {
  return <button className={`nav-item ${active ? 'active' : ''}`} onClick={onClick}><span className="nav-icon">{icon}</span>{label}</button>;
}

function DashboardView({ sites, cameras, onlineSites, onlineAgents, onCreate, onOpenSites }: { sites: Site[]; cameras: Camera[]; onlineSites: number; onlineAgents: number; onCreate: () => void; onOpenSites: () => void }) {
  const monitoredStorage = sites.filter(site => site.agent_status === 'online' && site.storage_total_bytes > 0);
  const freeBytes = monitoredStorage.reduce((total, site) => total + site.storage_free_bytes, 0);
  const unhealthy = sites.filter(site => site.agent_status === 'offline' || site.agent_health_error || (site.agent_status === 'online' && !site.storage_ok));
  return <>
    <section className="metrics" aria-label="Indicateurs">
      <Metric icon="◇" color="teal" label="Sites configurés" value={String(sites.length)} detail={`${onlineSites} connecté${onlineSites > 1 ? 's' : ''}`} />
      <Metric icon="◉" color="blue" label="Caméras" value={String(cameras.length)} detail={`${cameras.filter(c => c.enabled).length} activées`} />
      <Metric icon="▣" color="purple" label="Stockage disponible" value={monitoredStorage.length ? formatBytes(freeBytes) : '—'} detail={`${monitoredStorage.length} agent${monitoredStorage.length > 1 ? 's' : ''} surveillé${monitoredStorage.length > 1 ? 's' : ''}`} />
      <Metric icon="⌁" color="amber" label="Agents actifs" value={String(onlineAgents)} detail={`${sites.filter(s => s.agent_status === 'offline').length} hors ligne`} />
    </section>
    {unhealthy.length > 0 && <section className="health-warning"><span>!</span><div><strong>{unhealthy.length} site{unhealthy.length > 1 ? 's nécessitent' : ' nécessite'} une vérification</strong><small>{unhealthy.map(site => `${site.name}: ${site.agent_health_error || (site.agent_status === 'offline' ? 'agent hors ligne' : 'stockage indisponible')}`).join(' · ')}</small></div></section>}
    <section className="panel">
      <div className="panel-heading"><div><h2>Vos sites</h2><p>Surveillez les connexions et accédez aux caméras.</p></div><button className="text-button" onClick={onOpenSites}>Voir tous les sites →</button></div>
      {sites.length ? <div className="site-list">{sites.slice(0, 5).map(site => <SiteRow key={site.id} site={site} onClick={onOpenSites} />)}</div> : <EmptyState title="Aucun site pour le moment" text="Créez votre premier site pour connecter un agent et ajouter vos caméras locales." action="Créer mon premier site" onClick={onCreate} />}
    </section>
  </>;
}

function Metric({ icon, color, label, value, detail }: { icon: string; color: string; label: string; value: string; detail: string }) {
  return <article className="metric-card"><span className={`metric-symbol ${color}`}>{icon}</span><div><p>{label}</p><strong>{value}</strong><small>{detail}</small></div></article>;
}

function SiteRow({ site, onClick }: { site: Site; onClick: () => void }) {
  const status = site.agent_status === 'online' ? 'Connecté' : site.agent_status === 'offline' ? 'Hors ligne' : 'À installer';
  return <article className="site-row">
    <span className="site-avatar">{site.name.slice(0, 2).toUpperCase()}</span><div className="site-name"><strong>{site.name}</strong><small>{site.storage_ok && site.storage_total_bytes ? `${formatBytes(site.storage_free_bytes)} libres · ${site.recording_workers} enregistrement${site.recording_workers > 1 ? 's' : ''}` : site.location || 'Localisation non définie'}</small></div>
    <div className="camera-count"><span>◉</span><strong>{site.camera_count}</strong><small>caméra{site.camera_count > 1 ? 's' : ''}</small></div>
    <span className={`status ${site.agent_status}`}><i />{status}</span><button className="row-action" onClick={onClick} aria-label={`Ouvrir ${site.name}`}>→</button>
  </article>;
}

function SitesView({ sites, selected, onSelect, onDeploy, onCreate }: { sites: Site[]; selected: string; onSelect: (id: string) => void; onDeploy: (site: Site) => void; onCreate: () => void }) {
  if (!sites.length) return <section className="panel"><EmptyState title="Aucun site" text="Un site représente un lieu équipé de caméras et de son propre agent local." action="Créer un site" onClick={onCreate} /></section>;
  return <section className="site-grid">{sites.map(site => <article className={`site-card ${selected === site.id ? 'selected' : ''}`} key={site.id}><span className="site-avatar large">{site.name.slice(0, 2).toUpperCase()}</span><span className={`status ${site.agent_status}`}><i />{site.agent_status === 'online' ? 'Connecté' : site.agent_status === 'offline' ? 'Hors ligne' : 'Agent à installer'}</span><strong>{site.name}</strong><small>{site.location || 'Aucune localisation'}</small><div className="site-summary"><span>{site.camera_count} caméra{site.camera_count > 1 ? 's' : ''}</span><span>{modeLabels[site.default_recording_mode]}</span></div>{site.agent_status === 'online' && <div className={`storage-health ${site.storage_ok ? 'ok' : 'error'}`}><b>{site.storage_ok ? formatBytes(site.storage_free_bytes) : 'Stockage indisponible'}</b><small>{site.storage_ok ? `libres sur ${formatBytes(site.storage_total_bytes)}` : site.agent_health_error || 'Vérifiez le montage local'}</small></div>}<div className="site-actions"><button onClick={() => onSelect(site.id)}>Voir les caméras</button><button onClick={() => onDeploy(site)}>{site.agent_status === 'not_enrolled' ? 'Installer l’agent' : 'Réinstaller l’agent'}</button></div></article>)}</section>;
}

function LiveView({ sites, cameras, selectedSiteID, onSiteChange, onGetStream, onPTZ, onConfigure }: { sites: Site[]; cameras: Camera[]; selectedSiteID: string; onSiteChange: (id: string) => void; onGetStream: (cameraID: string) => Promise<StreamInfo>; onPTZ: PTZControl; onConfigure: () => void }) {
  const [layout, setLayout] = useState<'grid' | 'focus'>('grid');
  const [focusedCameraID, setFocusedCameraID] = useState('');
  const [autoStart, setAutoStart] = useState(true);
  const [talkNotice, setTalkNotice] = useState(false);
  const available = cameras.filter(camera => camera.site_id === selectedSiteID && camera.enabled);
  const focused = available.find(camera => camera.id === focusedCameraID) || available[0];

  useEffect(() => {
    if (!available.some(camera => camera.id === focusedCameraID)) setFocusedCameraID(available[0]?.id || '');
  }, [selectedSiteID, cameras.length]);

  if (!sites.length) return <section className="panel"><EmptyState title="Aucun site configuré" text="Créez un site et ajoutez une caméra avant d’ouvrir la vue en direct." /></section>;
  return <section className="live-console">
    <header className="live-commandbar">
      <div className="live-site-select"><span className="live-dot" /><label><small>Site surveillé</small><select value={selectedSiteID} onChange={event => onSiteChange(event.target.value)}>{sites.map(site => <option value={site.id} key={site.id}>{site.name}</option>)}</select></label><b>{available.length} en ligne</b></div>
      <div className="live-layout-actions"><label className="compact-toggle"><input type="checkbox" checked={autoStart} onChange={event => setAutoStart(event.target.checked)} /><span />Démarrage auto</label><div className="segmented"><button className={layout === 'grid' ? 'active' : ''} onClick={() => setLayout('grid')} aria-label="Afficher la mosaïque">▦ <span>Mosaïque</span></button><button className={layout === 'focus' ? 'active' : ''} onClick={() => setLayout('focus')} aria-label="Afficher une caméra">▣ <span>Focus</span></button></div><button className="live-configure" onClick={onConfigure}>⚙ Configurer</button></div>
    </header>
    {talkNotice && <div className="talk-notice"><span>🎙</span><div><strong>Audio bidirectionnel non activé</strong><small>L’écoute du son fonctionne. Pour parler vers la caméra, CloudNVR doit ajouter une passerelle locale compatible avec le backchannel de son fabricant.</small></div><button onClick={() => setTalkNotice(false)}>×</button></div>}
    {available.length ? <div className={`live-grid ${layout}`}>
      {(layout === 'focus' && focused ? [focused] : available).map(camera => <LiveCameraTile key={camera.id} camera={camera} autoStart={autoStart} focused={layout === 'focus'} onFocus={() => { setFocusedCameraID(camera.id); setLayout('focus'); }} onGetStream={onGetStream} onPTZ={onPTZ} onTalk={() => setTalkNotice(true)} />)}
      {layout === 'focus' && available.length > 1 && <aside className="live-camera-strip">{available.map(camera => <button key={camera.id} className={camera.id === focused?.id ? 'active' : ''} onClick={() => setFocusedCameraID(camera.id)}><i /><span>{camera.name}</span><small>{camera.ptz_enabled ? 'PTZ · ' : ''}{modeLabels[camera.recording_mode]}</small></button>)}</aside>}
    </div> : <section className="panel"><EmptyState title="Aucune caméra disponible" text="Activez une caméra sur ce site ou vérifiez sa configuration réseau." action="Configurer les caméras" onClick={onConfigure} /></section>}
  </section>;
}

function LiveCameraTile({ camera, autoStart, focused, onFocus, onGetStream, onPTZ, onTalk }: { camera: Camera; autoStart: boolean; focused: boolean; onFocus: () => void; onGetStream: (cameraID: string) => Promise<StreamInfo>; onPTZ: PTZControl; onTalk: () => void }) {
  const [stream, setStream] = useState<StreamInfo | null>(null);
  const [protocol, setProtocol] = useState<LiveProtocol>('webrtc');
  const [muted, setMuted] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const frame = useRef<HTMLElement>(null);
  const player = useRef<HTMLIFrameElement>(null);

  async function start() {
    if (loading || stream) return;
    setLoading(true); setError('');
    try { setStream(await onGetStream(camera.id)); }
    catch (err) { setError(err instanceof Error ? err.message : 'Flux indisponible'); }
    finally { setLoading(false); }
  }
  useEffect(() => { if (autoStart) void start(); }, [autoStart, camera.id]);
  const playerURL = stream ? mediaPlayerURL(liveProtocolURL(stream, protocol), true) : '';

  async function toggleAudio() {
    const video = player.current?.contentDocument?.querySelector('video');
    if (!video) {
      setError('Le lecteur audio n’est pas encore prêt.');
      return;
    }
    const nextMuted = !muted;
    video.muted = nextMuted;
    video.volume = 1;
    if (!nextMuted) {
      try {
        await video.play();
      } catch {
        video.muted = true;
        setMuted(true);
        setError('Le navigateur a bloqué le son. Touchez de nouveau le bouton audio.');
        return;
      }
    }
    setError('');
    setMuted(nextMuted);
  }

  function selectProtocol(value: LiveProtocol) {
    setMuted(true);
    setProtocol(value);
  }

  return <article className={`live-tile ${focused ? 'focused' : ''}`} ref={frame}>
    <div className="live-video">
      {playerURL ? <iframe ref={player} key={playerURL} src={playerURL} title={`Direct ${camera.name}`} allow="autoplay; fullscreen" /> : <div className="live-idle"><span>{loading ? '◌' : '◉'}</span><strong>{loading ? 'Connexion au direct…' : camera.name}</strong><small>{error || 'WebRTC faible latence'}</small><button onClick={() => void start()} disabled={loading}>{loading ? 'Connexion…' : 'Ouvrir le direct'}</button></div>}
      <div className="live-overlay-top"><span className="live-status"><i />DIRECT</span><strong>{camera.name}</strong><span className={`live-recording ${camera.recording_mode}`}>{camera.manual_recording ? '● REC' : modeLabels[camera.recording_mode]}</span></div>
      {stream && camera.ptz_enabled && <PTZPad onMove={(pan, tilt, zoom) => onPTZ(camera, pan, tilt, zoom)} onSetHome={() => onPTZ(camera, 0, 0, 0, 'set_home')} onGotoHome={() => onPTZ(camera, 0, 0, 0, 'goto_home')} />}
      {stream && <div className="live-player-controls"><button className={muted ? '' : 'active'} onClick={() => void toggleAudio()} title={muted ? 'Activer le son' : 'Couper le son'}>{muted ? '🔇' : '🔊'}</button><button onClick={onTalk} title="Parler vers la caméra">🎙</button><button className={protocol === 'webrtc' ? 'active' : ''} onClick={() => selectProtocol('webrtc')}>{stream.webrtc_mode === 'agent_direct' ? 'Direct maison' : 'WebRTC VPS'}</button>{stream.agent_webrtc_url && <button className={protocol === 'cloud_webrtc' ? 'active' : ''} onClick={() => selectProtocol('cloud_webrtc')}>Secours VPS</button>}<button className={protocol === 'hls' ? 'active' : ''} onClick={() => selectProtocol('hls')}>HLS</button><button onClick={onFocus} title="Afficher en grand">▣</button><button onClick={() => void frame.current?.requestFullscreen()} title="Plein écran">⛶</button><button onClick={() => setStream(null)} title="Fermer le flux">■</button></div>}
    </div>
    <footer><div><strong>{camera.name}</strong><small>{camera.access_mode === 'agent' ? 'Via agent local' : 'Accès direct'} · {camera.ptz_enabled ? 'PTZ disponible' : 'Position fixe'}</small></div><span>{camera.recording_mode === 'disabled' ? 'Sans enregistrement' : `${camera.local_retention_days} j local`}</span></footer>
  </article>;
}

function mediaPlayerURL(path: string, muted: boolean, controls = false) {
  const url = new URL(path, window.location.origin);
  url.searchParams.set('muted', muted ? 'true' : 'false');
  url.searchParams.set('autoplay', 'true');
  url.searchParams.set('controls', controls ? 'true' : 'false');
  url.searchParams.set('playsInline', 'true');
  return `${url.pathname}${url.search}`;
}

function liveProtocolURL(stream: StreamInfo, protocol: LiveProtocol) {
  if (protocol === 'hls') return stream.hls_url;
  if (protocol === 'cloud_webrtc') return stream.cloud_webrtc_url || stream.webrtc_url;
  return stream.webrtc_url;
}

function PTZPad({ onMove, onSetHome, onGotoHome }: { onMove: (pan: number, tilt: number, zoom: number) => Promise<void>; onSetHome: () => Promise<void>; onGotoHome: () => Promise<void> }) {
  const repeat = useRef<number | null>(null);
  const [homeBusy, setHomeBusy] = useState(false);
  function move(pan: number, tilt: number, zoom: number) { void onMove(pan, tilt, zoom).catch(() => undefined); }
  function stop() { if (repeat.current !== null) window.clearInterval(repeat.current); repeat.current = null; }
  function start(pan: number, tilt: number, zoom: number) {
    stop(); move(pan, tilt, zoom);
    repeat.current = window.setInterval(() => move(pan, tilt, zoom), 420);
  }
  async function setHome() {
    if (!window.confirm('Enregistrer la position actuelle comme position d’accueil de cette caméra ?')) return;
    setHomeBusy(true);
    try { await onSetHome(); } finally { setHomeBusy(false); }
  }
  async function gotoHome() {
    setHomeBusy(true);
    try { await onGotoHome(); } finally { setHomeBusy(false); }
  }
  useEffect(() => stop, []);
  const bind = (pan: number, tilt: number, zoom: number) => ({ onPointerDown: () => start(pan, tilt, zoom), onPointerUp: stop, onPointerCancel: stop, onPointerLeave: stop, onKeyDown: (event: ReactKeyboardEvent<HTMLButtonElement>) => { if (event.key === 'Enter' || event.key === ' ') move(pan, tilt, zoom); } });
  return <div className="ptz-overlay modern" aria-label="Commandes PTZ"><div className="ptz-direction"><button {...bind(0, .55, 0)} aria-label="Incliner vers le haut">↑</button><button {...bind(-.55, 0, 0)} aria-label="Tourner à gauche">←</button><button {...bind(.55, 0, 0)} aria-label="Tourner à droite">→</button><button {...bind(0, -.55, 0)} aria-label="Incliner vers le bas">↓</button></div><div className="ptz-zoom"><button {...bind(0, 0, .55)} aria-label="Zoom avant">＋</button><span>ZOOM</span><button {...bind(0, 0, -.55)} aria-label="Zoom arrière">−</button></div><div className="ptz-home"><button disabled={homeBusy} onClick={() => void gotoHome()} title="Aller à la position d’accueil"><span>⌂</span>Accueil</button><button disabled={homeBusy} onClick={() => void setHome()} title="Définir la position actuelle comme accueil"><span>◎</span>Définir</button></div></div>;
}

function CamerasView({ sites, selectedSite, selectedSiteID, cameras, onSiteChange, onCreate, onUpdate, onGetStream, onManualRecording, onPTZ }: { sites: Site[]; selectedSite?: Site; selectedSiteID: string; cameras: Camera[]; onSiteChange: (id: string) => void; onCreate: () => void; onUpdate: (event: FormEvent<HTMLFormElement>, camera: Camera) => void; onGetStream: (cameraID: string) => Promise<StreamInfo>; onManualRecording: (camera: Camera) => void; onPTZ: PTZControl }) {
  if (!sites.length) return <section className="panel"><EmptyState title="Créez d’abord un site" text="Chaque caméra doit appartenir à un site disposant d’un agent local." /></section>;
  return <>
    <div className="view-toolbar"><div><span>Site affiché</span><select value={selectedSiteID} onChange={e => onSiteChange(e.target.value)}>{sites.map(site => <option key={site.id} value={site.id}>{site.name}</option>)}</select></div><p>{selectedSite?.location || 'Réseau local'} · {cameras.length} caméra{cameras.length > 1 ? 's' : ''}</p></div>
    {cameras.length ? <section className="camera-grid">{cameras.map(camera => <CameraCard key={camera.id} camera={camera} onUpdate={onUpdate} onGetStream={onGetStream} onManualRecording={onManualRecording} onPTZ={onPTZ} />)}</section> : <section className="panel"><EmptyState title="Aucune caméra sur ce site" text="Utilisez une adresse RTSP locale accessible par l’agent du site." action="Ajouter une caméra" onClick={onCreate} /></section>}
  </>;
}

function CameraCard({ camera, onUpdate, onGetStream, onManualRecording, onPTZ }: { camera: Camera; onUpdate: (event: FormEvent<HTMLFormElement>, camera: Camera) => void; onGetStream: (cameraID: string) => Promise<StreamInfo>; onManualRecording: (camera: Camera) => void; onPTZ: PTZControl }) {
  const [editing, setEditing] = useState(false);
  const [stream, setStream] = useState<StreamInfo | null>(null);
  const [protocol, setProtocol] = useState<LiveProtocol>('webrtc');
  const [streamError, setStreamError] = useState('');
  const [starting, setStarting] = useState(false);

  async function startStream() {
    setStarting(true);
    setStreamError('');
    try { setStream(await onGetStream(camera.id)); }
    catch (err) { setStreamError(err instanceof Error ? err.message : 'Lecture impossible'); }
    finally { setStarting(false); }
  }

  const playerURL = stream && mediaPlayerURL(liveProtocolURL(stream, protocol), true, true);
  return <article className="camera-card">
    <div className={`camera-preview ${stream ? 'playing' : ''}`}>
      {playerURL
        ? <iframe key={playerURL} src={playerURL} title={`Flux ${camera.name} en ${protocol.toUpperCase()}`} allow="autoplay; fullscreen" />
        : <div className="preview-idle"><span>◉</span><strong>{camera.enabled ? 'Flux en direct' : 'Caméra désactivée'}</strong><small>{streamError || (camera.access_mode === 'direct' ? 'Relayée par le serveur cloud' : 'Relayée par l’agent local')}</small><button onClick={startStream} disabled={!camera.enabled || starting}>{starting ? 'Connexion…' : 'Lire la caméra'}</button></div>}
      {stream && <div className="player-toolbar"><button className={protocol === 'webrtc' ? 'active' : ''} onClick={() => setProtocol('webrtc')}>{stream.webrtc_mode === 'agent_direct' ? 'Direct maison' : 'WebRTC VPS'}</button>{stream.agent_webrtc_url && <button className={protocol === 'cloud_webrtc' ? 'active' : ''} onClick={() => setProtocol('cloud_webrtc')}>Secours VPS</button>}<button className={protocol === 'hls' ? 'active' : ''} onClick={() => setProtocol('hls')}>HLS secours</button><button onClick={() => setStream(null)} aria-label="Arrêter le flux">■</button></div>}
      {stream && camera.ptz_enabled && <PTZPad onMove={(pan, tilt, zoom) => onPTZ(camera, pan, tilt, zoom)} onSetHome={() => onPTZ(camera, 0, 0, 0, 'set_home')} onGotoHome={() => onPTZ(camera, 0, 0, 0, 'goto_home')} />}
    </div>
    <div className="camera-card-body"><div className="camera-title"><div><h3>{camera.name}</h3><span className={`mode-badge ${camera.recording_mode}`}>{modeLabels[camera.recording_mode]}</span><span className={`access-badge ${camera.access_mode}`}>{camera.access_mode === 'direct' ? 'Accès direct' : 'Via agent'}</span>{camera.ptz_enabled && <span className="ptz-badge">PTZ</span>}</div><button onClick={() => setEditing(!editing)}>{editing ? 'Fermer' : 'Modifier'}</button></div>{editing ? <form className="policy-form" onSubmit={e => onUpdate(e, camera)}><div className="form-grid compact"><div className="span-2"><Field label="Nom de la caméra"><input name="name" required defaultValue={camera.name} /></Field></div><div className="span-2"><Field label="Adresse RTSP"><input name="stream_url" type="url" required defaultValue={camera.stream_url} placeholder="rtsp://utilisateur:motdepasse@192.168.1.51:554/stream" /></Field></div><div className="span-2"><Field label="Accès au réseau de la caméra"><select name="access_mode" defaultValue={camera.access_mode}><option value="agent">Via l’agent local du site</option><option value="direct">Directement depuis le serveur CloudNVR</option></select></Field></div><Field label="Mode"><select name="recording_mode" defaultValue={camera.recording_mode}>{Object.entries(modeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field><Field label="Transfert"><select name="transfer_policy" defaultValue={camera.transfer_policy}>{Object.entries(transferLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field><Field label="Rétention locale"><input name="local_retention_days" type="number" min="0" defaultValue={camera.local_retention_days} /></Field><Field label="Rétention cloud"><input name="cloud_retention_days" type="number" min="0" defaultValue={camera.cloud_retention_days} /></Field></div><fieldset className="ptz-config"><legend>Contrôle motorisé ONVIF</legend><label className="toggle"><input name="ptz_enabled" type="checkbox" defaultChecked={camera.ptz_enabled} /><span />Activer le PTZ pour cette caméra</label><div className="form-grid compact"><Field label="Port ONVIF"><input name="ptz_port" type="number" min="1" max="65535" defaultValue={configuredONVIFPort(camera.ptz_endpoint)} /></Field><Field label="Utilisateur ONVIF"><input name="ptz_username" defaultValue={camera.ptz_username} autoComplete="username" /></Field><div className="span-2"><Field label="Adresse du service"><input name="ptz_endpoint" type="url" defaultValue={camera.ptz_endpoint} placeholder="http://192.168.1.50/onvif/device_service" /></Field></div><Field label="Mot de passe ONVIF"><input name="ptz_password" type="password" placeholder="Inchangé si vide" autoComplete="new-password" /></Field></div><small>Le port est appliqué automatiquement à l’adresse du service. Tapo utilise généralement le port 2020.</small></fieldset><p className="secret-help">Les adresses et mots de passe restent chiffrés ou protégés. Les commandes PTZ passent automatiquement par l’agent pour une caméra distante.</p><label className="toggle"><input name="enabled" type="checkbox" defaultChecked={camera.enabled} /><span />Caméra activée</label><button className="primary-button small" type="submit">Enregistrer les modifications</button></form> : <div className="camera-details"><span><b>{camera.local_retention_days} j</b> en local</span><span><b>{camera.cloud_retention_days} j</b> dans le cloud</span><span>{transferLabels[camera.transfer_policy]}</span></div>}</div>
    {camera.recording_mode === 'manual' && <button className={`manual-record-button ${camera.manual_recording ? 'active' : ''}`} onClick={() => onManualRecording(camera)}><i />{camera.manual_recording ? 'Arrêter l’enregistrement' : 'Démarrer l’enregistrement'}</button>}
  </article>;
}

function RecordingsView({ recordings, sites, cameras, onPrepare, onExport, onExportRange }: { recordings: Recording[]; sites: Site[]; cameras: Camera[]; onPrepare: (recordingID: string) => Promise<string>; onExport: (recording: Recording, from: number, to: number) => Promise<void>; onExportRange: (cameraID: string, from: Date, to: Date) => Promise<void> }) {
  const [siteID, setSiteID] = useState('');
  const [cameraID, setCameraID] = useState('');
  const [date, setDate] = useState(() => localDateKey(recordings[0]?.started_at || new Date().toISOString()));
  const [startTime, setStartTime] = useState('');
  const [endTime, setEndTime] = useState('');
  const [selectedID, setSelectedID] = useState('');
  const [cursorSeconds, setCursorSeconds] = useState(0);
  const [seekVersion, setSeekVersion] = useState(0);
  const [playbackRate, setPlaybackRate] = useState(1);
  const [timelineZoom, setTimelineZoom] = useState(1);
  const [clipStart, setClipStart] = useState(0);
  const [clipEnd, setClipEnd] = useState(30);
  const [exporting, setExporting] = useState(false);
  const [rangeStart, setRangeStart] = useState('');
  const [rangeEnd, setRangeEnd] = useState('');
  const [rangeExporting, setRangeExporting] = useState(false);
  const timelineScrollRef = useRef<HTMLDivElement>(null);
  const prefetchedRecordings = useRef(new Set<string>());
  const visible = recordings
    .filter(recording => {
      const recordingDate = localDateKey(recording.started_at);
      const recordingTime = localTimeKey(recording.started_at);
      return (!siteID || recording.site_id === siteID)
        && (!cameraID || recording.camera_id === cameraID)
        && (!date || recordingDate === date)
        && (!startTime || recordingTime >= startTime)
        && (!endTime || recordingTime <= endTime);
    })
    .sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime());
  const filteredCameras = cameras.filter(camera => !siteID || camera.site_id === siteID);
  const totalBytes = visible.reduce((sum, recording) => sum + recording.size_bytes, 0);
  const selected = visible.find(recording => recording.id === selectedID) || visible[0];
  const timelineCameras = filteredCameras.filter(camera => (!cameraID || camera.id === cameraID) && visible.some(recording => recording.camera_id === camera.id));
  const selectedStart = selected ? secondsOfDay(selected.started_at) : 0;
  const activeCursor = selected && selected.id === selectedID ? cursorSeconds : selectedStart;
  const playbackOffset = selected ? Math.max(0, activeCursor - selectedStart) : 0;
  const selectedDuration = selected ? recordingDurationSeconds(selected) : 1;
  const chronological = selected ? visible
    .filter(recording => recording.camera_id === selected.camera_id)
    .sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime()) : [];
  const nextRecording = selected ? chronological[chronological.findIndex(recording => recording.id === selected.id) + 1] : undefined;
  const previousRecording = selected ? chronological[chronological.findIndex(recording => recording.id === selected.id) - 1] : undefined;
  const overviewDuration = 86400 / timelineZoom;
  const overviewStart = Math.max(0, Math.min(86400 - overviewDuration, activeCursor - overviewDuration / 2));
  const overviewEnd = overviewStart + overviewDuration;
  const availableDates = Array.from(new Set(recordings
    .filter(recording => (!siteID || recording.site_id === siteID) && (!cameraID || recording.camera_id === cameraID))
    .map(recording => localDateKey(recording.started_at))))
    .sort((a, b) => b.localeCompare(a));

  useEffect(() => {
    const scroll = timelineScrollRef.current;
    if (!scroll || !selected) return;
    const frame = requestAnimationFrame(() => {
      scroll.scrollTop = Math.max(0, (activeCursor / 86400) * scroll.scrollHeight - scroll.clientHeight / 2);
    });
    return () => cancelAnimationFrame(frame);
  }, [selected?.id, date]);

  useEffect(() => {
    setClipStart(0);
    setClipEnd(Math.min(30, selectedDuration));
  }, [selected?.id, selectedDuration]);

  async function exportClip() {
    if (!selected || clipEnd <= clipStart) return;
    setExporting(true);
    try { await onExport(selected, clipStart, clipEnd); }
    finally { setExporting(false); }
  }

  async function exportRange() {
    if (!selected || !date || !rangeStart || !rangeEnd) return;
    const from = new Date(`${date}T${rangeStart}:00`);
    const to = new Date(`${date}T${rangeEnd}:00`);
    setRangeExporting(true);
    try { await onExportRange(selected.camera_id, from, to); }
    finally { setRangeExporting(false); }
  }

  function chooseRecordingAt(seconds: number, targetCameraID?: string) {
    const activeCameraID = targetCameraID || selected?.camera_id || cameraID || timelineCameras[0]?.id;
    const candidates = visible.filter(recording => recording.camera_id === activeCameraID);
    if (!candidates.length) return;
    const containing = candidates.find(recording => {
      const start = secondsOfDay(recording.started_at);
      const end = recording.ended_at ? secondsOfDay(recording.ended_at) : start + 60;
      return seconds >= start && seconds <= end;
    });
    const recording = containing || candidates.reduce((nearest, candidate) => Math.abs(secondsOfDay(candidate.started_at) - seconds) < Math.abs(secondsOfDay(nearest.started_at) - seconds) ? candidate : nearest);
    const start = secondsOfDay(recording.started_at);
    const end = recording.ended_at ? secondsOfDay(recording.ended_at) : start + 60;
    setCursorSeconds(Math.max(start, Math.min(seconds, end)));
    setSelectedID(recording.id);
    setSeekVersion(version => version + 1);
  }

  function playNextRecording() {
    if (!selected) return;
    if (!nextRecording) return;
    setSelectedID(nextRecording.id);
    setCursorSeconds(secondsOfDay(nextRecording.started_at));
    setSeekVersion(version => version + 1);
  }

  function prefetchNearbyRecordings() {
    for (const recording of [previousRecording, nextRecording]) {
      if (!recording || recording.playback_url || prefetchedRecordings.current.has(recording.id)) continue;
      prefetchedRecordings.current.add(recording.id);
      void onPrepare(recording.id).catch(() => prefetchedRecordings.current.delete(recording.id));
    }
  }

  function selectAdjacent(recording?: Recording) {
    if (!recording) return;
    setSelectedID(recording.id); setCursorSeconds(secondsOfDay(recording.started_at)); setSeekVersion(version => version + 1);
  }

  function shiftDay(days: number) {
    const next = new Date(`${date}T12:00:00`);
    next.setDate(next.getDate() + days);
    setDate(localDateKey(next.toISOString())); setSelectedID('');
  }
  function resetFilters() {
    setSiteID(''); setCameraID(''); setDate(localDateKey(new Date().toISOString())); setStartTime(''); setEndTime(''); setSelectedID('');
  }
  function timelinePosition(event: { currentTarget: HTMLDivElement; clientX: number; clientY: number }) {
    const rect = event.currentTarget.getBoundingClientRect();
    const seconds = Math.max(0, Math.min(86399, ((event.clientY - rect.top) / rect.height) * 86400));
    const lane = Math.max(0, Math.min(timelineCameras.length - 1, Math.floor(((event.clientX - rect.left) / rect.width) * timelineCameras.length)));
    return { seconds, cameraID: timelineCameras[lane]?.id };
  }
  return <>
    <section className="recording-toolbar">
      <div className="recording-filters">
        <label>Site<select value={siteID} onChange={event => { setSiteID(event.target.value); setCameraID(''); }}><option value="">Tous les sites</option>{sites.map(site => <option key={site.id} value={site.id}>{site.name}</option>)}</select></label>
        <label>Caméra<select value={cameraID} onChange={event => setCameraID(event.target.value)}><option value="">Toutes les caméras</option>{filteredCameras.map(camera => <option key={camera.id} value={camera.id}>{camera.name}</option>)}</select></label>
        <label>Date<span className="date-stepper"><button type="button" onClick={() => shiftDay(-1)}>‹</button><input type="date" value={date} onChange={event => { setDate(event.target.value); setSelectedID(''); }} /><button type="button" onClick={() => shiftDay(1)}>›</button></span></label>
        <label>De<input type="time" value={startTime} onChange={event => setStartTime(event.target.value)} /></label>
        <label>À<input type="time" value={endTime} onChange={event => setEndTime(event.target.value)} /></label>
        <button className="filter-reset" type="button" onClick={resetFilters}>Réinitialiser</button>
      </div>
      <p><strong>{visible.length}</strong> segment{visible.length !== 1 ? 's' : ''} · {formatBytes(totalBytes)}</p>
    </section>
    {availableDates.length > 0 && <section className="recording-days"><div><strong>Jours enregistrés</strong><small>Choisissez une journée</small></div><div>{availableDates.slice(0, 14).map(day => <button type="button" className={date === day ? 'active' : ''} key={day} onClick={() => { setDate(day); setSelectedID(''); }}><b>{new Date(`${day}T12:00:00`).toLocaleDateString('fr-FR', { weekday: 'short' })}</b><span>{new Date(`${day}T12:00:00`).toLocaleDateString('fr-FR', { day: '2-digit', month: 'short' })}</span></button>)}</div></section>}
    {selected && <section className="recording-overview">
      <header><div><p className="eyebrow">Navigation rapide</p><strong>{selected.camera_name}</strong><small>{formatClock(overviewStart)} – {formatClock(overviewEnd)}</small></div><div className="overview-cameras">{timelineCameras.map(camera => <button className={camera.id === selected.camera_id ? 'active' : ''} key={camera.id} onClick={() => chooseRecordingAt(activeCursor, camera.id)}><i />{camera.name}</button>)}</div><div className="overview-zoom"><small>Zoom</small>{[1, 2, 4, 8].map(value => <button className={timelineZoom === value ? 'active' : ''} key={value} onClick={() => setTimelineZoom(value)}>×{value}</button>)}</div></header>
      <div className="overview-ruler"><span>{formatClock(overviewStart)}</span><span>{formatClock(overviewStart + overviewDuration / 4)}</span><span>{formatClock(overviewStart + overviewDuration / 2)}</span><span>{formatClock(overviewStart + overviewDuration * .75)}</span><span>{formatClock(overviewEnd)}</span></div>
      <div className="overview-track" role="slider" tabIndex={0} aria-label="Naviguer dans la journée" aria-valuemin={Math.round(overviewStart)} aria-valuemax={Math.round(overviewEnd)} aria-valuenow={Math.round(activeCursor)} onPointerDown={event => { const rect = event.currentTarget.getBoundingClientRect(); chooseRecordingAt(overviewStart + ((event.clientX - rect.left) / rect.width) * overviewDuration, selected.camera_id); }} onKeyDown={event => { if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); chooseRecordingAt(activeCursor + (event.key === 'ArrowRight' ? 10 : -10), selected.camera_id); } }}>
        {visible.filter(recording => recording.camera_id === selected.camera_id).map(recording => { const start = secondsOfDay(recording.started_at); const end = start + recordingDurationSeconds(recording); const clippedStart = Math.max(start, overviewStart); const clippedEnd = Math.min(end, overviewEnd); if (clippedEnd <= clippedStart) return null; return <button key={recording.id} className={`${recording.source} ${recording.id === selected.id ? 'selected' : ''}`} style={{ left: `${((clippedStart - overviewStart) / overviewDuration) * 100}%`, width: `${Math.max(.18, ((clippedEnd - clippedStart) / overviewDuration) * 100)}%` }} onClick={event => { event.stopPropagation(); chooseRecordingAt(start, selected.camera_id); }} title={`${formatClock(start)} · ${formatDuration(recording)}`} />; })}
        <i className="overview-playhead" style={{ left: `${((activeCursor - overviewStart) / overviewDuration) * 100}%` }}><span>{formatClock(activeCursor)}</span></i>
      </div>
    </section>}
    {selected ? <section className="history-layout">
      <article className="recording-viewer">
        <div className="recording-viewer-head"><div><p className="eyebrow">Lecture sélectionnée</p><h2>{selected.camera_name}</h2><small>{selected.site_name} · {new Date(selected.started_at).toLocaleString('fr-FR', { dateStyle: 'long', timeStyle: 'short' })}</small></div><span className={`storage-badge ${selected.source}`}>{selected.source === 'cloud' ? 'Cloud' : 'Agent local'}</span></div>
        <div className="recording-stage">{selected.playback_url ? <RecordingVideo key={`${selected.id}-${seekVersion}`} src={selected.playback_url} startOffset={playbackOffset} playbackRate={playbackRate} onTimeUpdate={seconds => setCursorSeconds(selectedStart + seconds)} onEnded={playNextRecording} onReady={prefetchNearbyRecordings} /> : <AgentRecordingPlayer key={`${selected.id}-${seekVersion}`} recording={selected} onPrepare={onPrepare} startOffset={playbackOffset} playbackRate={playbackRate} autoLoad onTimeUpdate={seconds => setCursorSeconds(selectedStart + seconds)} onEnded={playNextRecording} onReady={prefetchNearbyRecordings} />}</div>
        <div className="recording-viewer-meta"><span>Durée <strong>{formatDuration(selected)}</strong></span><span>Taille <strong>{formatBytes(selected.size_bytes)}</strong></span><span>Début <strong>{new Date(selected.started_at).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</strong></span></div>
        <div className="playback-tools"><div><button type="button" disabled={!previousRecording} onClick={() => selectAdjacent(previousRecording)}>‹ Segment</button><button type="button" onClick={() => chooseRecordingAt(activeCursor - 10, selected.camera_id)}>− 10 s</button><button type="button" onClick={() => chooseRecordingAt(activeCursor + 10, selected.camera_id)}>+ 10 s</button><button type="button" disabled={!nextRecording} onClick={() => selectAdjacent(nextRecording)}>Segment ›</button></div><div><small>Vitesse</small>{[0.5, 1, 2, 4].map(rate => <button type="button" className={playbackRate === rate ? 'active' : ''} key={rate} onClick={() => setPlaybackRate(rate)}>×{rate}</button>)}</div></div>
        <section className="clip-exporter"><div className="clip-export-head"><div><strong>Exporter un extrait</strong><small>Sélection dans le segment actuel · maximum 2 heures</small></div><span>{formatRelativeTime(clipStart)} → {formatRelativeTime(clipEnd)} <b>{Math.round(clipEnd - clipStart)} s</b></span></div><div className="clip-track"><i style={{ left: `${(clipStart / selectedDuration) * 100}%`, width: `${Math.max(0, ((clipEnd - clipStart) / selectedDuration) * 100)}%` }} /><input aria-label="Début de l’extrait" type="range" min="0" max={selectedDuration} step="1" value={clipStart} onChange={event => setClipStart(Math.min(Number(event.target.value), clipEnd - 1))} /><input aria-label="Fin de l’extrait" type="range" min="0" max={selectedDuration} step="1" value={clipEnd} onChange={event => setClipEnd(Math.max(Number(event.target.value), clipStart + 1))} /></div><div className="clip-actions"><button type="button" onClick={() => setClipStart(Math.min(Math.floor(playbackOffset), clipEnd - 1))}>Début au curseur</button><button type="button" onClick={() => setClipEnd(Math.max(Math.ceil(playbackOffset), clipStart + 1))}>Fin au curseur</button><button type="button" className="export-button" onClick={() => void exportClip()} disabled={exporting || clipEnd <= clipStart}>{exporting ? 'Création du MP4…' : '⇩ Exporter le MP4'}</button></div></section>
        <section className="range-exporter"><div><strong>Exporter plusieurs segments</strong><small>Une seule vidéo, jusqu’à 2 heures consécutives</small></div><label>Début<input type="time" value={rangeStart} onChange={event => setRangeStart(event.target.value)} /></label><label>Fin<input type="time" value={rangeEnd} onChange={event => setRangeEnd(event.target.value)} /></label><button type="button" className="export-button" disabled={rangeExporting || !rangeStart || !rangeEnd} onClick={() => void exportRange()}>{rangeExporting ? 'Assemblage…' : '⇩ Exporter la plage'}</button></section>
        <div className="selected-recording-time"><span>{formatClock(activeCursor)}</span><div><strong>{new Date(`${date}T12:00:00`).toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })}</strong><small>Lecture continue des segments de {selected.camera_name}</small></div></div>
      </article>
      <section className="vertical-timeline-panel">
        <div className="timeline-heading"><div><p className="eyebrow">Historique précis</p><h2>Heures et minutes</h2></div><span className="timeline-current-time">{formatClock(activeCursor)}</span></div>
        <div className="camera-lane-head"><span>Heure</span><div style={{ gridTemplateColumns: `repeat(${timelineCameras.length}, minmax(72px, 1fr))` }}>{timelineCameras.map(camera => <b className={selected.camera_id === camera.id ? 'active' : ''} key={camera.id} title={camera.name}>{camera.name}</b>)}</div></div>
        <div className="vertical-timeline-scroll" ref={timelineScrollRef}><div className="vertical-hours-canvas" role="slider" tabIndex={0} aria-label="Timeline des enregistrements" aria-valuemin={0} aria-valuemax={86399} aria-valuenow={Math.round(activeCursor)}
          onPointerDown={event => { event.currentTarget.setPointerCapture(event.pointerId); const point = timelinePosition(event); setCursorSeconds(point.seconds); }}
          onPointerMove={event => { if (!event.currentTarget.hasPointerCapture(event.pointerId)) return; setCursorSeconds(timelinePosition(event).seconds); }}
          onPointerUp={event => { const point = timelinePosition(event); event.currentTarget.releasePointerCapture(event.pointerId); chooseRecordingAt(point.seconds, point.cameraID); }}
          onKeyDown={event => { if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return; event.preventDefault(); chooseRecordingAt(activeCursor + (event.key === 'ArrowDown' ? 60 : -60)); }}>
          {Array.from({ length: 24 }, (_, hour) => {
            const hourStart = hour * 3600;
            const hourEnd = hourStart + 3600;
            return <div className="vertical-hour" key={hour}><div className="hour-label"><strong>{String(hour).padStart(2, '0')}:00</strong><span>15</span><span>30</span><span>45</span></div><div className="hour-camera-lanes" style={{ gridTemplateColumns: `repeat(${timelineCameras.length}, minmax(72px, 1fr))` }}>
              {timelineCameras.map(camera => <div className={`hour-camera-lane ${selected.camera_id === camera.id ? 'active' : ''}`} key={camera.id}>{visible.filter(recording => recording.camera_id === camera.id).map(recording => {
                const start = secondsOfDay(recording.started_at);
                const end = Math.min(86400, start + recordingDurationSeconds(recording));
                const overlapStart = Math.max(start, hourStart);
                const overlapEnd = Math.min(end, hourEnd);
                if (overlapEnd <= overlapStart) return null;
                return <button type="button" key={recording.id} className={`minute-segment ${recording.source} ${recording.id === selected.id ? 'selected' : ''}`} style={{ top: `${((overlapStart - hourStart) / 3600) * 100}%`, height: `${Math.max(1.2, ((overlapEnd - overlapStart) / 3600) * 100)}%` }} title={`${recording.camera_name} · ${formatClock(start)} – ${formatClock(end)} · ${formatDuration(recording)}`} onClick={event => { event.stopPropagation(); chooseRecordingAt(start, camera.id); }} />;
              })}</div>)}
            </div></div>;
          })}
          <i className="vertical-playhead" style={{ top: `${(activeCursor / 86400) * 100}%` }}><span>{formatClock(activeCursor)}</span></i>
        </div></div>
        <div className="timeline-legend"><span><i className="recorded" />Segment cloud</span><span><i className="agent-segment" />Segment agent</span><small>Glissez verticalement, puis relâchez sur la minute recherchée.</small></div>
      </section>
    </section> : <section className="panel"><EmptyState title="Aucun enregistrement" text="Aucun segment ne correspond aux filtres sélectionnés." /></section>}
  </>;
}

function RecordingVideo({ src, startOffset, playbackRate, onTimeUpdate, onEnded, onReady, onStart }: { src: string; startOffset: number; playbackRate: number; onTimeUpdate?: (seconds: number) => void; onEnded?: () => void; onReady?: () => void; onStart?: () => void }) {
  const video = useRef<HTMLVideoElement>(null);
  useEffect(() => { if (video.current) video.current.playbackRate = playbackRate; }, [playbackRate]);
  return <video ref={video} controls autoPlay preload="auto" playsInline src={src} onLoadedMetadata={event => { event.currentTarget.currentTime = startOffset; event.currentTarget.playbackRate = playbackRate; }} onCanPlay={onReady} onPlay={onStart} onTimeUpdate={event => onTimeUpdate?.(event.currentTarget.currentTime)} onEnded={onEnded} />;
}

function AgentRecordingPlayer({ recording, onPrepare, startOffset = 0, playbackRate, autoLoad, onTimeUpdate, onEnded, onReady, onStart }: { recording: Recording; onPrepare: (recordingID: string) => Promise<string>; startOffset?: number; playbackRate: number; autoLoad: boolean; onTimeUpdate?: (seconds: number) => void; onEnded?: () => void; onReady?: () => void; onStart?: () => void }) {
  const [playbackURL, setPlaybackURL] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const requested = useRef(false);
  async function load() {
    if (requested.current) return;
    requested.current = true;
    setLoading(true); setError('');
    onStart?.();
    try { setPlaybackURL(await onPrepare(recording.id)); }
    catch (err) { requested.current = false; setError(err instanceof Error ? err.message : 'Lecture impossible'); }
    finally { setLoading(false); }
  }
  useEffect(() => { if (autoLoad) void load(); }, [autoLoad]);
  if (playbackURL) return <RecordingVideo src={playbackURL} startOffset={startOffset} playbackRate={playbackRate} onTimeUpdate={onTimeUpdate} onEnded={onEnded} onReady={onReady} onStart={onStart} />;
  return <div><span>{loading ? '↻' : '▶'}</span><strong>{loading ? 'Récupération depuis l’agent…' : 'Lire cet enregistrement local'}</strong><small>{error || (loading ? 'Le délai dépend de la taille du fichier et de la connexion du site.' : 'Le fichier sera envoyé temporairement et de manière sécurisée.')}</small><button className="local-play-button" onClick={() => void load()} disabled={loading}>{loading ? 'Préparation…' : 'Charger et lire'}</button></div>;
}

function formatBytes(bytes: number) {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} Ko`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} Mo`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} Go`;
}
function localDateKey(value: string) {
  const date = new Date(value);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}
function localTimeKey(value: string) {
  const date = new Date(value);
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}
function secondsOfDay(value: string) {
  const date = new Date(value);
  return date.getHours() * 3600 + date.getMinutes() * 60 + date.getSeconds();
}
function formatClock(seconds: number) {
  const safe = Math.max(0, Math.min(86400, Math.round(seconds)));
  return `${String(Math.floor(safe / 3600)).padStart(2, '0')}:${String(Math.floor((safe % 3600) / 60)).padStart(2, '0')}`;
}
function formatRelativeTime(seconds: number) {
  const safe = Math.max(0, Math.round(seconds));
  return `${String(Math.floor(safe / 60)).padStart(2, '0')}:${String(safe % 60).padStart(2, '0')}`;
}
function formatDuration(recording: Recording) {
  if (!recording.ended_at) return 'En cours';
  const seconds = recordingDurationSeconds(recording);
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes} min ${String(seconds % 60).padStart(2, '0')} s`;
}
function recordingDurationSeconds(recording: Recording) {
  if (!recording.ended_at) return 60;
  return Math.max(1, Math.round((new Date(recording.ended_at).getTime() - new Date(recording.started_at).getTime()) / 1000));
}
function EmptyState({ title, text, action, onClick }: { title: string; text: string; action?: string; onClick?: () => void }) { return <div className="empty-state"><span>⌁</span><h3>{title}</h3><p>{text}</p>{action && <button className="primary-button" onClick={onClick}>{action}</button>}</div>; }

function PairingScreen({ onClaim }: { onClaim: (name: string) => Promise<void> }) {
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true); setError('');
    const name = String(new FormData(event.currentTarget).get('device_name') || 'Mon iPhone');
    try { await onClaim(name); }
    catch (err) { setError(err instanceof Error ? err.message : 'Ce QR code n’est plus valide.'); }
    finally { setLoading(false); }
  }
  return <main className="login-screen pairing-screen"><section className="login-card pairing-card"><div className="brand login-brand"><span className="brand-mark"><span /></span><span>CloudNVR</span></div><span className="phone-mark">▦</span><p className="eyebrow">Appairage sécurisé</p><h1>Autoriser cet iPhone</h1><p>Ce téléphone pourra accéder à votre administration pendant 180 jours, sans saisir la clé principale. Vous pourrez révoquer cet accès à tout moment.</p>{error && <div className="alert error-alert"><span>!</span>{error}</div>}<form onSubmit={submit}><Field label="Nom de l’appareil"><input name="device_name" defaultValue="Mon iPhone" required autoFocus /></Field><button className="primary-button full" type="submit" disabled={loading}>{loading ? 'Autorisation…' : 'Autoriser cet appareil'}</button></form><small>Le QR code ne peut être utilisé qu’une seule fois et expire après 10 minutes.</small></section></main>;
}

function InstallSuccess({ onOpen }: { onOpen: () => void }) {
  return <main className="login-screen pairing-screen"><section className="login-card pairing-card"><div className="brand login-brand"><span className="brand-mark"><span /></span><span>CloudNVR</span></div><div className="success-mark">✓</div><p className="eyebrow">iPhone autorisé</p><h1>Installer CloudNVR</h1><ol className="install-steps"><li><b>1</b><span>Dans Safari, touchez le bouton <strong>Partager</strong>.</span></li><li><b>2</b><span>Choisissez <strong>Sur l’écran d’accueil</strong>.</span></li><li><b>3</b><span>Validez avec <strong>Ajouter</strong>.</span></li></ol><button className="primary-button full" onClick={onOpen}>Ouvrir CloudNVR</button><small>Votre session restera enregistrée dans l’application installée.</small></section></main>;
}

function MobileModal({ pairing, devices, loading, error, onRegenerate, onRevoke, onClose }: { pairing: (MobilePairing & { qr: string }) | null; devices: DeviceSession[]; loading: boolean; error: string; onRegenerate: () => void; onRevoke: (id: string) => void; onClose: () => void }) {
  const localURL = pairing?.pairing_url.includes('localhost') || pairing?.pairing_url.includes('127.0.0.1');
  return <Modal title="Installer l’app mobile" subtitle="Scannez ce QR code avec l’appareil photo de l’iPhone." onClose={onClose}><div className="mobile-pairing">{loading && <div className="qr-loading"><span className="loader" /><p>Création d’un accès temporaire…</p></div>}{error && <div className="alert error-alert"><span>!</span>{error}</div>}{pairing && !loading && <><div className="qr-frame"><img src={pairing.qr} alt="QR code d’appairage CloudNVR" /></div><p className="qr-expiry">Usage unique · expire à {new Date(pairing.expires_at).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' })}</p>{localURL && <p className="pairing-warning">L’adresse publique est actuellement configurée sur localhost. Pour scanner depuis l’iPhone, définissez <code>PUBLIC_URL</code> avec le domaine HTTPS ou l’adresse réseau de CloudNVR.</p>}<ol className="compact-steps"><li>Scannez avec l’appareil photo.</li><li>Autorisez l’iPhone sur la page ouverte.</li><li>Dans Safari : Partager → Sur l’écran d’accueil.</li></ol><button className="secondary-button full" onClick={onRegenerate}>Générer un nouveau QR code</button></>}</div><section className="device-section"><div><h3>Appareils autorisés</h3><small>{devices.length} appareil{devices.length > 1 ? 's' : ''} actif{devices.length > 1 ? 's' : ''}</small></div>{devices.length ? <div className="device-list">{devices.map(device => <article key={device.id}><span>▦</span><div><strong>{device.name}</strong><small>Vu {new Date(device.last_seen_at).toLocaleDateString('fr-FR')}</small></div><button onClick={() => onRevoke(device.id)}>Révoquer</button></article>)}</div> : <p className="no-devices">Aucun téléphone appairé pour le moment.</p>}</section></Modal>;
}

function Login({ onSubmit, error }: { onSubmit: (event: FormEvent<HTMLFormElement>) => void; error: string }) {
  return <main className="login-screen"><section className="login-card"><div className="brand login-brand"><span className="brand-mark"><span /></span><span>CloudNVR</span></div><p className="eyebrow">Administration sécurisée</p><h1>Accéder à votre NVR</h1><p>Entrez la clé administrateur définie dans le fichier <code>.env</code> du serveur.</p>{error && <div className="alert error-alert">{error}</div>}<form onSubmit={onSubmit}><Field label="Clé administrateur"><input name="api_key" type="password" autoComplete="current-password" required autoFocus placeholder="Votre clé ADMIN_API_KEY" /></Field><button className="primary-button full" type="submit">Se connecter</button></form><small>La clé reste uniquement dans cette session de navigateur.</small></section></main>;
}
function LoadingScreen() { return <main className="loading-screen"><div className="brand login-brand"><span className="brand-mark"><span /></span><span>CloudNVR</span></div><span className="loader" /><p>Connexion au NVR…</p></main>; }

function SiteModal({ onClose, onSubmit }: { onClose: () => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return <Modal title="Créer un site" subtitle="Un site correspond à un réseau local équipé de caméras." onClose={onClose}><form className="modal-form" onSubmit={onSubmit}><Field label="Nom du site"><input name="name" required placeholder="Maison, Bureau, Entrepôt…" autoFocus /></Field><Field label="Localisation"><input name="location" placeholder="Ville ou description" /></Field><Field label="Mode par défaut"><select name="default_recording_mode" defaultValue="hybrid">{Object.entries(modeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field><div className="modal-actions"><button type="button" className="secondary-button" onClick={onClose}>Annuler</button><button className="primary-button" type="submit">Créer le site</button></div></form></Modal>;
}

function CameraModal({ sites, siteID, onSiteChange, onClose, onSubmit }: { sites: Site[]; siteID: string; onSiteChange: (id: string) => void; onClose: () => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  const [accessMode, setAccessMode] = useState<AccessMode>('agent');
  return <Modal title="Ajouter une caméra" subtitle="Choisissez comment CloudNVR peut atteindre son adresse RTSP." onClose={onClose}><form className="modal-form" onSubmit={onSubmit}><Field label="Site"><select value={siteID} onChange={e => onSiteChange(e.target.value)}>{sites.map(site => <option key={site.id} value={site.id}>{site.name}</option>)}</select></Field><Field label="Nom de la caméra"><input name="name" required placeholder="Entrée principale" autoFocus /></Field><Field label="Adresse RTSP"><input name="stream_url" type="url" required placeholder="rtsp://utilisateur:motdepasse@192.168.1.50:554/stream" /></Field><Field label="Accès au réseau de la caméra"><select name="access_mode" value={accessMode} onChange={e => setAccessMode(e.target.value as AccessMode)}><option value="agent">Via l’agent local du site</option><option value="direct">Directement depuis le serveur CloudNVR</option></select></Field><p className={`access-hint ${accessMode}`}>{accessMode === 'direct' ? 'Aucun agent requis. Utilisez ce mode uniquement si la machine CloudNVR peut joindre directement l’IP de la caméra, sur le même LAN ou via un VPN.' : 'L’agent installé sur le site se chargera de joindre l’IP locale de la caméra.'}</p><div className="form-grid"><Field label="Mode d’enregistrement"><select name="recording_mode" defaultValue="hybrid">{Object.entries(modeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field><Field label="Transfert cloud"><select name="transfer_policy" defaultValue="events_and_manual">{Object.entries(transferLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field><Field label="Rétention locale (jours)"><input name="local_retention_days" type="number" min="0" defaultValue="7" /></Field><Field label="Rétention cloud (jours)"><input name="cloud_retention_days" type="number" min="0" defaultValue="30" /></Field></div><fieldset className="ptz-config"><legend>Caméra motorisée ONVIF</legend><label className="toggle"><input name="ptz_enabled" type="checkbox" /><span />Activer les commandes PTZ</label><div className="form-grid"><Field label="Port ONVIF"><input name="ptz_port" type="number" min="1" max="65535" defaultValue="80" /></Field><Field label="Utilisateur ONVIF"><input name="ptz_username" autoComplete="username" /></Field><div className="span-2"><Field label="Adresse du service (optionnelle)"><input name="ptz_endpoint" type="url" placeholder="http://192.168.1.50/onvif/device_service" /></Field></div><Field label="Mot de passe ONVIF"><input name="ptz_password" type="password" autoComplete="new-password" /></Field></div><small>L’adresse IP est reprise du flux RTSP si le service reste vide. Pour une caméra Tapo, le port ONVIF est généralement 2020.</small></fieldset><div className="modal-actions"><button type="button" className="secondary-button" onClick={onClose}>Annuler</button><button className="primary-button" type="submit">Ajouter la caméra</button></div></form></Modal>;
}

function EnrollmentModal({ enrollment, onClose }: { enrollment: Enrollment; onClose: () => void }) {
  const command = `CLOUD_URL=${enrollment.agent_environment.CLOUD_URL} SITE_ID=${enrollment.site.id} ENROLLMENT_TOKEN=${enrollment.enrollment_token}`;
  return <Modal title="Installation de l’agent" subtitle="Ce nouveau jeton est à usage unique. Le générer ne coupe pas un agent actuellement connecté." onClose={onClose}><div className="enrollment"><div className="success-mark">✓</div><h3>{enrollment.site.name}</h3><p>Utilisez ces variables sur la machine locale qui accédera aux caméras :</p><pre>{command}</pre><button className="secondary-button full" onClick={() => navigator.clipboard?.writeText(command)}>Copier la configuration</button><button className="primary-button full" onClick={onClose}>J’ai sauvegardé le jeton</button></div></Modal>;
}

function Modal({ title, subtitle, onClose, children }: { title: string; subtitle: string; onClose: () => void; children: ReactNode }) { return <div className="modal-backdrop" role="presentation" onMouseDown={e => e.target === e.currentTarget && onClose()}><section className="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title"><button className="modal-close" onClick={onClose} aria-label="Fermer">×</button><div className="modal-heading"><h2 id="modal-title">{title}</h2><p>{subtitle}</p></div>{children}</section></div>; }
function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }
