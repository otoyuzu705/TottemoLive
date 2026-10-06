export namespace main {
	
	export class CacheInfo {
	    dir: string;
	    enabled: boolean;
	    usedBytes: number;
	    freeBytes: number;
	    freeKnown: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CacheInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.enabled = source["enabled"];
	        this.usedBytes = source["usedBytes"];
	        this.freeBytes = source["freeBytes"];
	        this.freeKnown = source["freeKnown"];
	    }
	}
	export class PreviewResult {
	    url: string;
	    bandsUrl: string;
	    bands: number;
	    frames: number;
	    hopSec: number;
	    offsetDb: number;
	
	    static createFrom(source: any = {}) {
	        return new PreviewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.bandsUrl = source["bandsUrl"];
	        this.bands = source["bands"];
	        this.frames = source["frames"];
	        this.hopSec = source["hopSec"];
	        this.offsetDb = source["offsetDb"];
	    }
	}
	export class SourceInfo {
	    id: string;
	    path: string;
	    name: string;
	    durationSec: number;
	    sampleRate: number;
	    channels: number;
	
	    static createFrom(source: any = {}) {
	        return new SourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.durationSec = source["durationSec"];
	        this.sampleRate = source["sampleRate"];
	        this.channels = source["channels"];
	    }
	}
	export class WindowResult {
	    url: string;
	    startSec: number;
	    totalSec: number;
	
	    static createFrom(source: any = {}) {
	        return new WindowResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.startSec = source["startSec"];
	        this.totalSec = source["totalSec"];
	    }
	}

}

export namespace params {
	
	export class ParamSpec {
	    path: string;
	    label: string;
	    group: string;
	    kind: string;
	    unit: string;
	    min: number;
	    max: number;
	    step: number;
	    default: number;
	    scale: string;
	    options: string[];
	    advanced: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ParamSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.label = source["label"];
	        this.group = source["group"];
	        this.kind = source["kind"];
	        this.unit = source["unit"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.step = source["step"];
	        this.default = source["default"];
	        this.scale = source["scale"];
	        this.options = source["options"];
	        this.advanced = source["advanced"];
	    }
	}

}

export namespace project {
	
	export class Listener {
	    x: number;
	    y: number;
	    z: number;
	    yawDeg: number;
	
	    static createFrom(source: any = {}) {
	        return new Listener(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	        this.z = source["z"];
	        this.yawDeg = source["yawDeg"];
	    }
	}
	export class Output {
	    sampleRate: number;
	    bitDepth: number;
	    targetLufs: number;
	    ceilingDbTp: number;
	
	    static createFrom(source: any = {}) {
	        return new Output(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sampleRate = source["sampleRate"];
	        this.bitDepth = source["bitDepth"];
	        this.targetLufs = source["targetLufs"];
	        this.ceilingDbTp = source["ceilingDbTp"];
	    }
	}
	export class PA {
	    autoLevel: string;
	    inputLufs: number;
	    lowCutHz: number;
	    lowShelfHz: number;
	    lowShelfDb: number;
	    highShelfHz: number;
	    highShelfDb: number;
	    compThresholdDb: number;
	    compRatio: number;
	    compAttackMs: number;
	    compReleaseMs: number;
	    drive: number;
	
	    static createFrom(source: any = {}) {
	        return new PA(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.autoLevel = source["autoLevel"];
	        this.inputLufs = source["inputLufs"];
	        this.lowCutHz = source["lowCutHz"];
	        this.lowShelfHz = source["lowShelfHz"];
	        this.lowShelfDb = source["lowShelfDb"];
	        this.highShelfHz = source["highShelfHz"];
	        this.highShelfDb = source["highShelfDb"];
	        this.compThresholdDb = source["compThresholdDb"];
	        this.compRatio = source["compRatio"];
	        this.compAttackMs = source["compAttackMs"];
	        this.compReleaseMs = source["compReleaseMs"];
	        this.drive = source["drive"];
	    }
	}
	export class Reverb {
	    mix: number;
	    preDelayMs: number;
	    decayScale: number;
	    highDampHz: number;
	    lowCoherence: number;
	    lowDecayScale: number;
	    lowLevelDb: number;
	    lowCrossoverHz: number;
	    highDecayScale: number;
	    highDecayHz: number;
	
	    static createFrom(source: any = {}) {
	        return new Reverb(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mix = source["mix"];
	        this.preDelayMs = source["preDelayMs"];
	        this.decayScale = source["decayScale"];
	        this.highDampHz = source["highDampHz"];
	        this.lowCoherence = source["lowCoherence"];
	        this.lowDecayScale = source["lowDecayScale"];
	        this.lowLevelDb = source["lowLevelDb"];
	        this.lowCrossoverHz = source["lowCrossoverHz"];
	        this.highDecayScale = source["highDecayScale"];
	        this.highDecayHz = source["highDecayHz"];
	    }
	}
	export class Spatial {
	    hrirSet: string;
	    distanceRolloff: number;
	    airAbsorption: number;
	    directLevelDb: number;
	
	    static createFrom(source: any = {}) {
	        return new Spatial(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hrirSet = source["hrirSet"];
	        this.distanceRolloff = source["distanceRolloff"];
	        this.airAbsorption = source["airAbsorption"];
	        this.directLevelDb = source["directLevelDb"];
	    }
	}
	export class Sub {
	    enabled: string;
	    levelDb: number;
	    crossoverHz: number;
	
	    static createFrom(source: any = {}) {
	        return new Sub(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.levelDb = source["levelDb"];
	        this.crossoverHz = source["crossoverHz"];
	    }
	}
	export class Speaker {
	    id: string;
	    x: number;
	    y: number;
	    z: number;
	
	    static createFrom(source: any = {}) {
	        return new Speaker(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.x = source["x"];
	        this.y = source["y"];
	        this.z = source["z"];
	    }
	}
	export class Venue {
	    preset: string;
	    speakers: Speaker[];
	    subs: Speaker[];
	
	    static createFrom(source: any = {}) {
	        return new Venue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preset = source["preset"];
	        this.speakers = this.convertValues(source["speakers"], Speaker);
	        this.subs = this.convertValues(source["subs"], Speaker);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Source {
	    id: string;
	    path: string;
	    role: string;
	    gainDb: number;
	
	    static createFrom(source: any = {}) {
	        return new Source(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.role = source["role"];
	        this.gainDb = source["gainDb"];
	    }
	}
	export class Project {
	    version: number;
	    sources: Source[];
	    venue: Venue;
	    listener: Listener;
	    pa: PA;
	    sub: Sub;
	    spatial: Spatial;
	    reverb: Reverb;
	    output: Output;
	
	    static createFrom(source: any = {}) {
	        return new Project(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.sources = this.convertValues(source["sources"], Source);
	        this.venue = this.convertValues(source["venue"], Venue);
	        this.listener = this.convertValues(source["listener"], Listener);
	        this.pa = this.convertValues(source["pa"], PA);
	        this.sub = this.convertValues(source["sub"], Sub);
	        this.spatial = this.convertValues(source["spatial"], Spatial);
	        this.reverb = this.convertValues(source["reverb"], Reverb);
	        this.output = this.convertValues(source["output"], Output);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class SoundPresetInfo {
	    name: string;
	    readOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SoundPresetInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.readOnly = source["readOnly"];
	    }
	}
	
	
	
	

}

export namespace settings {
	
	export class Settings {
	    version: number;
	    cacheEnabled: boolean;
	    cacheDir: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.cacheEnabled = source["cacheEnabled"];
	        this.cacheDir = source["cacheDir"];
	    }
	}

}

export namespace venue {
	
	export class Preset {
	    id: string;
	    name: string;
	    widthM: number;
	    depthM: number;
	    speakers: project.Speaker[];
	    subs: project.Speaker[];
	    reverb: project.Reverb;
	    rt60Sec: number;
	    volumeM3: number;
	    q: number;
	
	    static createFrom(source: any = {}) {
	        return new Preset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.widthM = source["widthM"];
	        this.depthM = source["depthM"];
	        this.speakers = this.convertValues(source["speakers"], project.Speaker);
	        this.subs = this.convertValues(source["subs"], project.Speaker);
	        this.reverb = this.convertValues(source["reverb"], project.Reverb);
	        this.rt60Sec = source["rt60Sec"];
	        this.volumeM3 = source["volumeM3"];
	        this.q = source["q"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

