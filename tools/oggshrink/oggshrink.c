// oggshrink re-encodes an Ogg Vorbis stream at a given Vorbis quality, for
// compack's opt-in lossy OGG pass (ogg.lossy). It reads the stream on stdin
// and writes the new one on stdout. The channels are kept; sample rates above
// 48 kHz (content no one hears, and more than Minecraft plays) are halved
// until they are not. The output is deterministic: a fixed stream serial and
// no comments.
//
// usage: oggshrink <quality 0-10>
// exit:  0 ok, 1 usage or I/O error, 2 not Ogg Vorbis or undecodable,
//        3 unsupported (too many channels, or a chained stream whose channels
//        or rate change)

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <vorbis/vorbisenc.h>
#include <vorbis/vorbisfile.h>

#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#endif

#define MAX_RATE 48000
#define MAX_CHANNELS 8
#define MAX_STAGES 4
#define CHUNK 4096
#define TAPS 125
#define PI 3.14159265358979323846

// --- input: the whole stream in memory, read through vorbisfile callbacks

typedef struct {
	unsigned char *data;
	size_t len, pos;
} memfile;

static size_t mem_read(void *ptr, size_t size, size_t n, void *src) {
	memfile *m = src;
	size_t want = size * n, left = m->len - m->pos;
	if (size == 0) return 0;
	if (want > left) want = left - left % size;
	memcpy(ptr, m->data + m->pos, want);
	m->pos += want;
	return want / size;
}

static int mem_seek(void *src, ogg_int64_t off, int whence) {
	memfile *m = src;
	ogg_int64_t base = whence == SEEK_SET ? 0 : whence == SEEK_CUR ? (ogg_int64_t)m->pos : (ogg_int64_t)m->len;
	if (base + off < 0 || base + off > (ogg_int64_t)m->len) return -1;
	m->pos = (size_t)(base + off);
	return 0;
}

static long mem_tell(void *src) {
	return (long)((memfile *)src)->pos;
}

static unsigned char *read_all(FILE *f, size_t *len) {
	size_t cap = 1 << 20, n = 0;
	unsigned char *buf = malloc(cap);
	while (buf) {
		n += fread(buf + n, 1, cap - n, f);
		if (n < cap) break;
		unsigned char *grown = realloc(buf, cap * 2);
		if (!grown) free(buf);
		buf = grown;
		cap *= 2;
	}
	if (!buf || ferror(f)) {
		free(buf);
		return NULL;
	}
	*len = n;
	return buf;
}

// --- 2:1 decimation: a windowed-sinc low-pass just under the new Nyquist
// frequency, then every other sample. The filter delays its output by
// (TAPS-1)/2 input samples, a whole number of output samples with TAPS = 125:
// the first DELAY outputs are dropped and the stream is flushed with zeros,
// so the result stays aligned.

#define DELAY ((TAPS - 1) / 4)

static float taps[TAPS];

static void design_lowpass(void) {
	const double fc = 0.24; // cycles per input sample; the output Nyquist is 0.25
	const int m = TAPS - 1;
	double coef[TAPS], sum = 0;
	for (int i = 0; i < TAPS; i++) {
		double n = i - m / 2.0;
		double sinc = n == 0 ? 2 * fc : sin(2 * PI * fc * n) / (PI * n);
		double blackman = 0.42 - 0.5 * cos(2 * PI * i / m) + 0.08 * cos(4 * PI * i / m);
		coef[i] = sinc * blackman;
		sum += coef[i];
	}
	for (int i = 0; i < TAPS; i++) taps[i] = (float)(coef[i] / sum);
}

typedef struct {
	// Each sample is written twice, TAPS apart, so the last TAPS samples are
	// always one contiguous run starting at pos.
	float hist[MAX_CHANNELS][2 * TAPS];
	int pos;
	int phase; // 0: this input sample produces an output
	int skip;  // outputs still to drop (the filter delay)
} stage;

static int decimate(stage *s, int channels, float **in, int n, float **out) {
	int k = 0;
	for (int i = 0; i < n; i++) {
		for (int c = 0; c < channels; c++) s->hist[c][s->pos] = s->hist[c][s->pos + TAPS] = in[c][i];
		s->pos = (s->pos + 1) % TAPS;
		int emit = s->phase == 0;
		s->phase ^= 1;
		if (!emit) continue;
		if (s->skip > 0) {
			s->skip--;
			continue;
		}
		for (int c = 0; c < channels; c++) {
			const float *h = s->hist[c] + s->pos;
			float acc = 0;
			for (int t = 0; t < TAPS; t++) acc += taps[t] * h[t];
			out[c][k] = acc;
		}
		k++;
	}
	return k;
}

// --- output

static void write_page(const ogg_page *og) {
	if (fwrite(og->header, 1, (size_t)og->header_len, stdout) != (size_t)og->header_len ||
	    fwrite(og->body, 1, (size_t)og->body_len, stdout) != (size_t)og->body_len) {
		exit(1);
	}
}

typedef struct {
	int channels, stages;
	stage st[MAX_STAGES];
	float buf[2][MAX_CHANNELS][CHUNK];
	vorbis_dsp_state vd;
	vorbis_block vb;
	ogg_stream_state os;
} encoder;

static void drain(encoder *e) {
	ogg_packet op;
	ogg_page og;
	while (vorbis_analysis_blockout(&e->vd, &e->vb) == 1) {
		vorbis_analysis(&e->vb, NULL);
		vorbis_bitrate_addblock(&e->vb);
		while (vorbis_bitrate_flushpacket(&e->vd, &op)) {
			ogg_stream_packetin(&e->os, &op);
			while (ogg_stream_pageout(&e->os, &og)) write_page(&og);
		}
	}
}

// feed runs n frames through the decimation stages into the encoder.
static void feed(encoder *e, float **pcm, int n) {
	float *cur[MAX_CHANNELS], *next[MAX_CHANNELS];
	for (int c = 0; c < e->channels; c++) cur[c] = pcm[c];
	for (int s = 0; s < e->stages; s++) {
		for (int c = 0; c < e->channels; c++) next[c] = e->buf[s % 2][c];
		n = decimate(&e->st[s], e->channels, cur, n, next);
		for (int c = 0; c < e->channels; c++) cur[c] = next[c];
	}
	if (n == 0) return;
	float **dst = vorbis_analysis_buffer(&e->vd, n);
	for (int c = 0; c < e->channels; c++) memcpy(dst[c], cur[c], (size_t)n * sizeof(float));
	vorbis_analysis_wrote(&e->vd, n);
	drain(e);
}

static encoder enc;

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: oggshrink <quality 0-10> < in.ogg > out.ogg\n");
		return 1;
	}
	int quality = atoi(argv[1]);
	if (quality < 0) quality = 0;
	if (quality > 10) quality = 10;
#ifdef _WIN32
	_setmode(_fileno(stdin), _O_BINARY);
	_setmode(_fileno(stdout), _O_BINARY);
#endif

	memfile in = {0};
	in.data = read_all(stdin, &in.len);
	if (!in.data) return 1;
	ov_callbacks cb = {mem_read, mem_seek, NULL, mem_tell};
	OggVorbis_File vf;
	if (ov_open_callbacks(&in, &vf, NULL, 0, cb) < 0) return 2;

	vorbis_info *info = ov_info(&vf, -1);
	int channels = info->channels;
	long rate = info->rate;
	if (channels < 1 || channels > MAX_CHANNELS) return 3;
	for (int i = 0; i < ov_streams(&vf); i++) {
		vorbis_info *link = ov_info(&vf, i);
		if (link->channels != channels || link->rate != rate) return 3;
	}

	encoder *e = &enc;
	e->channels = channels;
	long out_rate = rate;
	while (out_rate > MAX_RATE && e->stages < MAX_STAGES) {
		out_rate /= 2;
		e->stages++;
	}
	for (int s = 0; s < e->stages; s++) e->st[s].skip = DELAY;
	design_lowpass();

	vorbis_info vi;
	vorbis_comment vc;
	vorbis_info_init(&vi);
	if (vorbis_encode_init_vbr(&vi, channels, out_rate, quality / 10.0f) != 0) return 3;
	vorbis_comment_init(&vc);
	vorbis_analysis_init(&e->vd, &vi);
	vorbis_block_init(&e->vd, &e->vb);
	ogg_stream_init(&e->os, 1);

	ogg_packet head, comments, books;
	ogg_page og;
	vorbis_analysis_headerout(&e->vd, &vc, &head, &comments, &books);
	ogg_stream_packetin(&e->os, &head);
	ogg_stream_packetin(&e->os, &comments);
	ogg_stream_packetin(&e->os, &books);
	while (ogg_stream_flush(&e->os, &og)) write_page(&og);

	for (;;) {
		float **pcm;
		int link;
		long n = ov_read_float(&vf, &pcm, CHUNK, &link);
		if (n == OV_HOLE) continue;
		if (n < 0) return 2;
		if (n == 0) break;
		feed(e, pcm, (int)n);
	}
	if (e->stages > 0) {
		// Flush the filters: enough zeros for the deepest stage's delay.
		static float zeros[MAX_CHANNELS][CHUNK];
		float *z[MAX_CHANNELS];
		for (int c = 0; c < channels; c++) z[c] = zeros[c];
		int left = (TAPS / 2 + 1) << e->stages;
		while (left > 0) {
			int n = left < CHUNK ? left : CHUNK;
			feed(e, z, n);
			left -= n;
		}
	}
	vorbis_analysis_wrote(&e->vd, 0);
	drain(e);
	while (ogg_stream_flush(&e->os, &og)) write_page(&og);
	if (fflush(stdout) != 0) return 1;

	ogg_stream_clear(&e->os);
	vorbis_block_clear(&e->vb);
	vorbis_dsp_clear(&e->vd);
	vorbis_comment_clear(&vc);
	vorbis_info_clear(&vi);
	ov_clear(&vf);
	free(in.data);
	return 0;
}
