"""Confirmed Lua source binding for the existing per-character launcher."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import tempfile
from contextlib import contextmanager


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def private(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('Owned private policy directory required')


def read(path, limit=8*1024*1024):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError('Owned private policy binding required')
        raw = stream.read(limit+1)
    if len(raw)>limit:
        raise ValueError('Policy binding exceeds bound')
    return raw


def optional(path):
    try:return read(path, 65536)
    except FileNotFoundError:return None


def write(path, raw):
    fd, temporary = tempfile.mkstemp(prefix='.binding-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw);stream.flush();os.fsync(stream.fileno())
        os.replace(temporary, path)
        fd = os.open(path.parent, os.O_RDONLY)
        try:os.fsync(fd)
        finally:os.close(fd)
    finally:
        if os.path.exists(temporary):os.unlink(temporary)


def packed(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def object_json(raw):
    def unique(rows):
        out={}
        for key,value in rows:
            if key in out:raise ValueError('Duplicate policy binding key')
            out[key]=value
        return out
    value=json.loads(raw,object_pairs_hook=unique,
        parse_constant=lambda _: (_ for _ in ()).throw(ValueError('Invalid policy binding number')))
    if not isinstance(value,dict):raise ValueError('Policy binding object required')
    return value


@contextmanager
def locked(root, guid):
    private(root);actor=Path(root)/str(guid);private(actor)
    fd=os.open(actor/'edit.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'r+') as stream:
        info=os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode&0o077:
            raise ValueError('Owned private edit lock required')
        fcntl.flock(stream,fcntl.LOCK_EX|fcntl.LOCK_NB)
        yield actor


def prepare(root, guid, identity, policy_file, release):
    """Resolve once before spawn; changed startup choices never inherit old edits."""
    source=Path(policy_file).read_bytes()
    anchor=sha(packed(dict(identity=identity, policyFile=str(Path(policy_file).absolute()),
                           sourceRevision=sha(source), release=release)))
    with locked(root,guid) as actor:
        selected=Path(policy_file);revision=sha(source)
        raw=optional(actor/'accepted.json')
        if raw is not None:
            accepted=object_json(raw)
            if accepted.get('anchor')==anchor:
                revision=accepted.get('revision')
                if not isinstance(revision,str) or re.fullmatch('[a-f0-9]{64}',revision) is None:
                    raise ValueError('Invalid accepted policy revision')
                selected=actor/(revision+'.lua')
                if sha(read(selected))!=revision:raise ValueError('Accepted policy source changed')
        # Invalidate previous-body promotion before this new process is entered.
        write(actor/'launch.json',packed(dict(anchor=anchor, sessionId=None, revision=revision)))
    return dict(anchor=anchor, revision=revision, policyFile=str(selected))


def ready(root, guid, launch, sid):
    if not isinstance(sid,str) or re.fullmatch('[A-Za-z0-9._-]{1,128}',sid) is None:
        raise ValueError('Exact startup session required')
    with locked(root,guid) as actor:
        expected=packed(dict(anchor=launch['anchor'],sessionId=None,revision=launch['revision']))
        if optional(actor/'launch.json')!=expected:raise ValueError('Policy launch changed')
        write(actor/'launch.json',packed(dict(anchor=launch['anchor'],sessionId=sid,revision=launch['revision'])))


def capture(actor, sid, expected):
    """Called under self-edit's existing actor lock, before entering the CAS."""
    raw=optional(actor/'launch.json')
    if raw is None:return None
    launch=object_json(raw)
    if launch.get('sessionId')!=sid:return None
    accepted=optional(actor/'accepted.json')
    current=object_json(accepted) if accepted else {}
    revision=current.get('revision') if current.get('anchor')==launch.get('anchor') else launch.get('revision')
    if revision!=expected:return None
    return raw,accepted,launch['anchor']


def promote(actor, captured, revision, sid):
    if captured is None:return False
    before,accepted,anchor=captured
    if optional(actor/'launch.json')!=before or optional(actor/'accepted.json')!=accepted:
        return False
    if not isinstance(revision,str) or re.fullmatch('[a-f0-9]{64}',revision) is None:return False
    if sha(read(actor/(revision+'.lua')))!=revision:raise ValueError('Confirmed policy source changed')
    write(actor/'accepted.json',packed(dict(anchor=anchor,revision=revision,confirmedSessionId=sid)))
    return True
