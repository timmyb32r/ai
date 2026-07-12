const $ = (sel) => document.querySelector(sel);

$('#submit-btn').addEventListener('click', async () => {
  const url = $('#url-input').value.trim();
  if (!url) return;

  $('#error-msg').classList.add('hidden');
  $('#submit-btn').disabled = true;

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
    pollJob(data.job_id);
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
  $('#url-input').focus();
});

async function pollJob(id) {
  showProgress();
  while (true) {
    try {
      const resp = await fetch('/api/status/' + id);
      if (!resp.ok) {
        showError('Failed to check status');
        return;
      }
      const job = await resp.json();
      updateProgress(job);
      if (job.status === 'done') { showDone(id); break; }
      if (job.status === 'error') { showError(job.error || 'Unknown error'); break; }
    } catch (e) {
      showError('Connection lost: ' + e.message);
      return;
    }
    await sleep(2000);
  }
}

function showProgress() {
  $('#input-section').classList.add('hidden');
  $('#progress-section').classList.remove('hidden');
  $('#done-section').classList.add('hidden');
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
  $('#error-msg').textContent = msg;
  $('#error-msg').classList.remove('hidden');
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
