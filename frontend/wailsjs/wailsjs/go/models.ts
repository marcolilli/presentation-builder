export namespace main {
	
	export class AppSettings {
	    markdownRoots: string[];
	    exportDirectory: string;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.markdownRoots = source["markdownRoots"];
	        this.exportDirectory = source["exportDirectory"];
	    }
	}
	export class Presentation {
	    name: string;
	    deckUrl: string;
	    notesUrl: string;
	    sourcePath: string;
	    builtAt: string;
	    canRebuild: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Presentation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.deckUrl = source["deckUrl"];
	        this.notesUrl = source["notesUrl"];
	        this.sourcePath = source["sourcePath"];
	        this.builtAt = source["builtAt"];
	        this.canRebuild = source["canRebuild"];
	    }
	}
	export class BootState {
	    baseUrl: string;
	    presentations: Presentation[];
	
	    static createFrom(source: any = {}) {
	        return new BootState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseUrl = source["baseUrl"];
	        this.presentations = this.convertValues(source["presentations"], Presentation);
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
	
	export class SearchResult {
	    name: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	    }
	}

}

