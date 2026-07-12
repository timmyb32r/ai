const $ = (sel) => document.querySelector(sel);
const SLEEP_MS = 2000;

$('#submit-btn').addEventListener('click', async () => {
  const url = $('#url-input').value.trim();
  if (!url) return;

  $('#error-msg').classList.add('hidden');
  $('#submit-btn').disabled = true;
  $('#submit-btn').textContent = 'Submitting...';

  try {
    const resp = await fetch('/api/transcribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    });
    const data = await resp.json();
    if (!resp.ok) {
      showError(data.error || 'Request failed');
      return;
    }
    showProgress();
    await pollJob(data.job_id);
  } catch (e) {
    showError('Network error: ' + e.message);
  }
});

$('#url-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') $('#submit-btn').click();
});

$('#new-btn').addEventListener('click', () => {
  $('#input-section').classList.remove('hidden');
  $('#progress-section').classList.add('hidden');
  $('#done-section').classList.add('hidden');
  $('#error-msg').classList.add('hidden');
  $('#url-input').value = '';
  $('#submit-btn').disabled = false;
  $('#submit-btn').textContent = 'Transcribe';
  $('#url-input').focus();
});

async function pollJob(id) {
  while (true) {
    let resp, job;
    try {
      resp = await fetch('/api/status/' + id);
      if (!resp.ok) {
        if (resp.status === 404) {
          // Job deleted from store (already completed earlier)
          // Try download anyway
          const dlResp = await fetch('/api/download/' + id);
          if (dlResp.ok) {
            showDone(id);
            return;
          }
        }
        showError('Job status check failed (HTTP ' + resp.status + ')');
        return;
      }
      job = await resp.json();
    } catch (e) {
      showError('Connection lost: ' + e.message);
      return;
    }

    updateProgress(job);

    if (job.status === 'done') {
      // Brief pause so user sees the 100% progress
      await sleep(800);
      showDone(id);
      return;
    }
    if (job.status === 'error') {
      showError(job.error || 'Unknown error');
      return;
    }
    await sleep(SLEEP_MS);
  }
}

function showProgress() {
  $('#input-section').classList.add('hidden');
  $('#progress-section').classList.remove('hidden');
  $('#done-section').classList.add('hidden');
  $('#progress-fill').style.width = '2%';
  $('#stage-label').textContent = 'Extracting audio...';
}

function updateProgress(job) {
  const pct = Math.round((job.progress || 0) * 100);
  $('#progress-fill').style.width = pct + '%';
  $('#stage-label').textContent = job.stage || 'Processing...';
}

function showDone(id) {
  $('#progress-section').classList.add('hidden');
  $('#done-section').classList.remove('hidden');
  $('#download-link').href = '/api/download/' + id;
}

function showError(msg) {
  $('#input-section').classList.remove('hidden');
  $('#progress-section').classList.add('hidden');
  $('#done-section').classList.add('hidden');
  $('#submit-btn').disabled = false;
  $('#submit-btn').textContent = 'Transcribe';
  $('#error-msg').textContent = msg;
  $('#error-msg').classList.remove('hidden');
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
