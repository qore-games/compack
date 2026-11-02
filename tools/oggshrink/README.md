# oggshrink

The Vorbis re-encoder behind compack's `-ogg-lossy`: reads Ogg Vorbis on stdin,
writes it re-encoded at a quality (0-10) on stdout, keeping the channels and
halving sample rates above 48 kHz.

```sh
oggshrink 4 < in.ogg > out.ogg
```

`./build.sh` builds it with `zig cc` for every compack target, statically
linked against libogg 1.3.5 and libvorbis 1.3.7 (downloaded and checked
against pinned SHA-256s), into `optim/bin/`. Licenses: `COPYING.libogg`,
`COPYING.libvorbis`.
